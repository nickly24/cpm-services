package calculate

import (
	"context"
	"errors"
	"fmt"
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

// Opt-in integration runs only against the fixed local Dev and a fresh scratch
// collection. Neither production nor the live calculate inbox can be selected.
func integrationStore(t *testing.T) *Store {
	t.Helper()
	uri := os.Getenv("CALCULATE_TEST_MONGO_URI")
	if uri == "" {
		t.Skip("CALCULATE_TEST_MONGO_URI not set; local Mongo integration is opt-in")
	}
	if !strings.Contains(uri, "@127.0.0.1:27018/cpm_dev") {
		t.Fatal("integration Mongo must be fixed local cpm_dev")
	}
	journal := true
	client, err := mongo.Connect(options.Client().ApplyURI(uri).SetMaxPoolSize(16).SetWriteConcern(&writeconcern.WriteConcern{W: "majority", Journal: &journal}))
	if err != nil {
		t.Fatal("cannot initialize local Mongo client")
	}
	collection := client.Database("cpm_dev").Collection("calculate_pool_check_" + leaseID())
	store := &Store{Collection: collection}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := collection.Drop(ctx); err != nil {
			t.Error("cannot remove isolated integration collection")
		}
		_ = client.Disconnect(ctx)
	})
	return store
}

func TestMongoConcurrentClaimsAreUniqueAndIndexedAgainstLargeFinishedHistory(t *testing.T) {
	store := integrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := store.EnsureIndexes(ctx); err != nil {
		t.Fatal(err)
	}
	const total = 80
	for i := 1; i <= total; i++ {
		m, _ := poolGradeMessage(t, i, "initial_grade")
		if inserted, err := store.Receive(ctx, m); err != nil || !inserted {
			t.Fatalf("cannot receive fixture %d: %v", i, err)
		}
		if inserted, err := store.Receive(ctx, m); err != nil || inserted {
			t.Fatal("duplicate receipt did not remain idempotent")
		}
	}
	history := make([]any, 2000)
	for i := range history {
		history[i] = bson.M{"_id": fmt.Sprintf("old-%d", i), "processing_kind": InitialGradeKind, "status": "ready", "received_at": time.Now().Add(-24 * time.Hour)}
	}
	if _, err := store.Collection.InsertMany(ctx, history); err != nil {
		t.Fatal(err)
	}
	assertIndexPlan(t, ctx, store, InitialGradeKind, "received")
	var mu sync.Mutex
	claims := map[string]int{}
	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				m, err := store.ClaimKind(ctx, time.Now().UTC(), leaseID(), InitialGradeKind)
				if noJob(err) {
					return
				}
				if err != nil {
					t.Error("concurrent claim failed")
					return
				}
				mu.Lock()
				claims[m.ID]++
				mu.Unlock()
				if m.ProcessingKind != InitialGradeKind || m.LeaseToken == "" || m.ProcessingAttempts != 1 {
					t.Error("claim lost processing identity")
				}
				if err := store.Finish(ctx, m, "ready", "", nil); err != nil {
					t.Error("leased job could not finish")
					return
				}
			}
		}()
	}
	workers.Wait()
	if len(claims) != total {
		t.Fatalf("claimed %d of %d jobs", len(claims), total)
	}
	for id, count := range claims {
		if count != 1 {
			t.Fatalf("job %s was claimed %d times", id, count)
		}
	}
	assertIndexPlan(t, ctx, store, InitialGradeKind, "received")
	if count, err := store.Collection.CountDocuments(ctx, bson.M{"status": "ready"}); err != nil || count != total+int64(len(history)) {
		t.Fatalf("completed history changed unexpectedly: %d %v", count, err)
	}
}

func assertIndexPlan(t *testing.T, ctx context.Context, store *Store, kind, status string) {
	t.Helper()
	filter := bson.M{"processing_kind": kind, "status": status, "available_at": bson.M{"$lte": time.Now().UTC()}}
	command := bson.D{{Key: "explain", Value: bson.D{{Key: "find", Value: store.Collection.Name()}, {Key: "filter", Value: filter}, {Key: "sort", Value: bson.D{{Key: "available_at", Value: 1}, {Key: "_id", Value: 1}}}, {Key: "hint", Value: "inbox_schedule"}, {Key: "limit", Value: 1}}}, {Key: "verbosity", Value: "executionStats"}}
	var explain bson.Raw
	if err := store.Collection.Database().RunCommand(ctx, command).Decode(&explain); err != nil {
		t.Fatal("cannot explain isolated claim query")
	}
	raw, _ := bson.MarshalExtJSON(explain, false, false)
	plan := string(raw)
	if strings.Contains(plan, `"stage":"SORT"`) || strings.Contains(plan, `"stage":"COLLSCAN"`) || !strings.Contains(plan, "inbox_schedule") {
		t.Fatalf("claim is not index-ordered: %s", plan)
	}
	var examined int64
	value := explain.Lookup("executionStats", "totalDocsExamined")
	if integer, ok := value.Int64OK(); ok {
		examined = integer
	} else if integer, ok := value.Int32OK(); ok {
		examined = int64(integer)
	} else {
		t.Fatal("explain omitted execution stats")
	}
	if examined > 1 {
		t.Fatalf("claim read historical jobs: %d documents examined", examined)
	}
	t.Logf("index-ordered claim with %d documents examined; no SORT/COLLSCAN", examined)
}

func TestMongoPendingLegacyMigrationLeaseRecoveryAndStaleOwnerFencing(t *testing.T) {
	store := integrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	now := time.Now().UTC()
	received, _ := poolGradeMessage(t, 1, "initial_grade")
	retrying, _ := poolGradeMessage(t, 2, "initial_grade")
	retrying.Status = "retrying"
	next := now.Add(time.Hour)
	retrying.NextAttemptAt = &next
	expired, _ := poolGradeMessage(t, 3, "regrade")
	expired.Status, expired.LeaseToken = "processing", "old-owner"
	until := now.Add(-time.Second)
	expired.LeaseUntil = &until
	finished, _ := poolGradeMessage(t, 4, "initial_grade")
	finished.Status = "ready"
	if _, err := store.Collection.InsertMany(ctx, []any{received, retrying, expired, finished}); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureIndexes(ctx); err != nil {
		t.Fatal(err)
	}
	now = time.Now().UTC()
	for _, original := range []Message{received, retrying, expired} {
		stored, err := store.Get(ctx, original.ID)
		if err != nil || stored.ProcessingKind != processingKind(original) || stored.AvailableAt == nil || stored.BodyHash != original.BodyHash || string(stored.Body) != string(original.Body) {
			t.Fatalf("legacy migration changed immutable receipt or lost schedule: %+v %v", stored, err)
		}
	}
	storedFinished, err := store.Get(ctx, finished.ID)
	if err != nil || storedFinished.AvailableAt != nil || storedFinished.ProcessingKind != "" {
		t.Fatal("finished history was rewritten by migration")
	}
	claimed, err := store.ClaimKind(ctx, now, "new-owner", RegradeKind)
	if err != nil || claimed.ID != expired.ID || claimed.LeaseToken != "new-owner" {
		t.Fatalf("expired lease was not recovered: %+v %v", claimed, err)
	}
	for _, write := range []func() error{
		func() error { return store.Checkpoint(ctx, expired, "stale-cursor", nil) },
		func() error { return store.Finish(ctx, expired, "ready", "", nil) },
		func() error { return store.Retry(ctx, expired, "stale-retry", now) },
	} {
		if !errors.Is(write(), ErrLeaseLost) {
			t.Fatal("old lease owner was allowed to change recovered work")
		}
	}
	if err := store.Finish(ctx, claimed, "ready", "", nil); err != nil {
		t.Fatal(err)
	}
	claimed, err = store.ClaimKind(ctx, now, leaseID(), InitialGradeKind)
	if err != nil || claimed.ID != received.ID {
		t.Fatal("migrated receipt was not scheduled")
	}
	if err := store.Finish(ctx, claimed, "ready", "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimKind(ctx, now, leaseID(), InitialGradeKind); !noJob(err) {
		t.Fatal("retry was claimed before its due time")
	}
	claimed, err = store.ClaimKind(ctx, next.Add(time.Second), leaseID(), InitialGradeKind)
	if err != nil || claimed.ID != retrying.ID {
		t.Fatal("due retry was not scheduled")
	}
	if err := store.Finish(ctx, claimed, "failed", "test", []byte(`{"saved":true}`)); err != nil {
		t.Fatal(err)
	}
	// A failed historical message without metadata can also be retried after
	// migration has already run; its checkpoint and raw body must survive.
	if _, err := store.Collection.UpdateOne(ctx, bson.M{"_id": retrying.ID}, bson.M{"$unset": bson.M{"processing_kind": ""}}); err != nil {
		t.Fatal(err)
	}
	if requeued, err := store.Requeue(ctx, retrying.ID); err != nil || !requeued {
		t.Fatal("historical failed message did not requeue")
	}
	claimed, err = store.ClaimKind(ctx, time.Now().UTC().Add(time.Second), leaseID(), InitialGradeKind)
	if err != nil || claimed.ID != retrying.ID || string(claimed.Output) != `{"saved":true}` {
		t.Fatal("operational retry lost its checkpoint or processing lane")
	}
}

func TestMongoRollingLegacyWorkerLeaseRemainsAuthoritative(t *testing.T) {
	store := integrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := store.EnsureIndexes(ctx); err != nil {
		t.Fatal(err)
	}
	m, _ := poolGradeMessage(t, 1, "initial_grade")
	if _, err := store.Receive(ctx, m); err != nil {
		t.Fatal(err)
	}
	until := time.Now().UTC().Add(time.Hour)
	// Simulate the old Claim, which updates lease but not available_at.
	if _, err := store.Collection.UpdateOne(ctx, bson.M{"_id": m.ID}, bson.M{"$set": bson.M{"status": "processing", "lease_token": "legacy-owner", "lease_until": until}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimKind(ctx, time.Now().UTC(), leaseID(), InitialGradeKind); !noJob(err) {
		t.Fatal("new worker stole an unexpired old-version lease")
	}
}
