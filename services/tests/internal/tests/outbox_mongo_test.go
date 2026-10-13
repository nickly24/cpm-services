package tests

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/writeconcern"
)

// Opt-in real Mongo test. It never reads/writes business collections, and only
// permits the dedicated loopback Dev database; the scratch collection is dropped.
func TestOutboxMongoConcurrencyRecoveryAndIndexedIdle(t *testing.T) {
	address := os.Getenv("OUTBOX_TEST_MONGO_URI")
	if address == "" {
		t.Skip("OUTBOX_TEST_MONGO_URI not set")
	}
	u, err := url.Parse(address)
	if err != nil || u.Host != "127.0.0.1:27018" || u.Path != "/cpm_dev" {
		t.Fatal("outbox integration only permits dedicated loopback cpm_dev")
	}
	journal := true
	client, err := mongo.Connect(options.Client().ApplyURI(address).SetServerSelectionTimeout(5 * time.Second).
		SetWriteConcern(&writeconcern.WriteConcern{W: "majority", Journal: &journal}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Disconnect(context.Background()) })
	collection := client.Database("cpm_dev").Collection("architecture_outbox_test_" + bson.NewObjectID().Hex())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := collection.Drop(ctx); err != nil {
			t.Error("scratch collection cleanup failed", err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := ensureCollectionOutbox(ctx, collection); err != nil {
		t.Fatal(err)
	}
	makePending := func() bson.ObjectID {
		id := bson.NewObjectID()
		_, err := collection.InsertOne(ctx, bson.M{"_id": id, "revision": int64(1), "outbox_pending": true,
			"outbox": []storedEvent{{ID: "first", Body: []byte(`{}`), RoutingKey: "tests.grade.requested.v1"}}})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}

	t.Run("exclusive-claims-expiry-and-stale-owner", func(t *testing.T) {
		id := makePending()
		claimed := make(chan leasedOutbox, 10)
		failures := make(chan error, 10)
		var wg sync.WaitGroup
		for range 10 {
			wg.Go(func() {
				record, err := claimOutbox(ctx, collection, 8, time.Now().UTC())
				if err == nil {
					claimed <- record
				} else if !errors.Is(err, mongo.ErrNoDocuments) {
					failures <- err
				}
			})
		}
		wg.Wait()
		close(claimed)
		close(failures)
		for err := range failures {
			t.Error(err)
		}
		if len(claimed) != 1 {
			t.Fatalf("one document simultaneously claimed %d times", len(claimed))
		}
		old := <-claimed
		if old.ID != id || len(old.Outbox) != 1 || old.LeaseToken == "" {
			t.Fatal("claim lost identity, payload or fencing token")
		}
		if _, err := collection.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": bson.M{"outbox_lease_until": time.Now().Add(-time.Second)}}); err != nil {
			t.Fatal(err)
		}
		fresh, err := claimOutbox(ctx, collection, 8, time.Now().UTC())
		if err != nil || fresh.LeaseToken == old.LeaseToken {
			t.Fatal("expired lease did not recover with a new fencing token", err)
		}
		if err := finishOutbox(ctx, old, []string{"first"}); !errors.Is(err, errOutboxLeaseLost) {
			t.Fatal("stale owner could delete newly claimed events", err)
		}
		if err := finishOutbox(ctx, fresh, []string{"first"}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("concurrent-enqueue-is-not-lost", func(t *testing.T) {
		for range 20 {
			id := makePending()
			record, err := claimOutbox(ctx, collection, 8, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			update := bson.M{"$set": bson.M{"revision": int64(2)}}
			enqueueOutbox(update, storedEvent{ID: "second", Body: []byte(`{}`), RoutingKey: "tests.content.changed.v1"})
			failures := make(chan error, 2)
			var wg sync.WaitGroup
			wg.Go(func() { _, err := collection.UpdateOne(ctx, bson.M{"_id": id}, update); failures <- err })
			wg.Go(func() { failures <- finishOutbox(ctx, record, []string{"first"}) })
			wg.Wait()
			close(failures)
			for err := range failures {
				if err != nil {
					t.Fatal(err)
				}
			}
			var saved struct {
				Pending bool          `bson:"outbox_pending"`
				Events  []storedEvent `bson:"outbox"`
			}
			if err := collection.FindOne(ctx, bson.M{"_id": id}).Decode(&saved); err != nil {
				t.Fatal(err)
			}
			if !saved.Pending || len(saved.Events) != 1 || saved.Events[0].ID != "second" {
				t.Fatal("enqueue racing with publisher cleanup lost an event", saved)
			}
			next, err := claimOutbox(ctx, collection, 8, time.Now().UTC())
			if err != nil || next.ID != id {
				t.Fatal("retained event cannot be claimed", err)
			}
			if err := finishOutbox(ctx, next, []string{"second"}); err != nil {
				t.Fatal(err)
			}
		}
	})

	t.Run("idempotent-backfill-preserves-active-lease", func(t *testing.T) {
		legacy := bson.NewObjectID()
		leased := makePending()
		record, err := claimOutbox(ctx, collection, 8, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		_, err = collection.InsertOne(ctx, bson.M{"_id": legacy, "revision": int64(7),
			"outbox": []storedEvent{{ID: "legacy", Body: []byte(`{}`), RoutingKey: "tests.grade.requested.v1"}}})
		if err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if err := ensureCollectionOutbox(ctx, collection); err != nil {
				t.Fatal(err)
			}
		}
		var saved bson.M
		if err := collection.FindOne(ctx, bson.M{"_id": leased}).Decode(&saved); err != nil {
			t.Fatal(err)
		}
		if saved["outbox_lease_token"] != record.LeaseToken || saved["revision"] != int64(1) {
			t.Fatal("backfill changed active lease or business revision")
		}
		pending, err := claimOutbox(ctx, collection, 8, time.Now().UTC())
		if err != nil || pending.ID != legacy {
			t.Fatal("legacy event was not migrated to indexed queue", err)
		}
		if err := finishOutbox(ctx, pending, []string{"legacy"}); err != nil {
			t.Fatal(err)
		}
		if err := finishOutbox(ctx, record, []string{"first"}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("idle-poll-does-not-scan-history", func(t *testing.T) {
		history := make([]any, 100)
		for i := range history {
			history[i] = bson.M{"outbox": bson.A{}, "outbox_pending": false}
		}
		if _, err := collection.InsertMany(ctx, history); err != nil {
			t.Fatal(err)
		}
		var result struct {
			Planner bson.M `bson:"queryPlanner"`
			Stats   struct {
				Docs     int64 `bson:"totalDocsExamined"`
				Returned int64 `bson:"nReturned"`
			} `bson:"executionStats"`
		}
		err := collection.Database().RunCommand(ctx, bson.D{{Key: "explain", Value: bson.M{
			"find": collection.Name(), "filter": bson.M{"outbox_pending": true, "$or": bson.A{
				bson.M{"outbox_lease_until": nil}, bson.M{"outbox_lease_until": bson.M{"$lte": time.Now().UTC()}},
			}}, "sort": bson.D{{Key: "outbox_lease_until", Value: 1}, {Key: "_id", Value: 1}}, "hint": outboxReadyIndex, "limit": 1,
		}}, {Key: "verbosity", Value: "executionStats"}}).Decode(&result)
		if err != nil {
			t.Fatal(err)
		}
		plan, _ := bson.MarshalExtJSON(result.Planner, false, false)
		if result.Stats.Docs != 0 || result.Stats.Returned != 0 || strings.Contains(string(plan), "COLLSCAN") || !strings.Contains(string(plan), outboxReadyIndex) {
			t.Fatalf("idle query scanned history: docs=%d returned=%d plan=%s", result.Stats.Docs, result.Stats.Returned, plan)
		}
		t.Logf("idle Explain: totalDocsExamined=%d; index=%s", result.Stats.Docs, outboxReadyIndex)
	})
}
