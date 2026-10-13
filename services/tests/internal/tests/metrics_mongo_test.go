package tests

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func TestMetricsMongoBacklogCountsAndOldestExcludeHistoricalRegrades(t *testing.T) {
	uri := os.Getenv("OUTBOX_TEST_MONGO_URI")
	if uri == "" {
		t.Skip("OUTBOX_TEST_MONGO_URI not set")
	}
	u, err := url.Parse(uri)
	if err != nil || u.Host != "127.0.0.1:27018" || u.Path != "/cpm_dev" {
		t.Fatal("metrics integration only permits dedicated loopback cpm_dev")
	}
	client, err := mongo.Connect(options.Client().ApplyURI(uri).SetServerSelectionTimeout(5 * time.Second))
	if err != nil {
		t.Fatal("cannot initialize local Mongo metrics fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	catalog := client.Database("cpm_dev").Collection("architecture_metrics_catalog_" + bson.NewObjectID().Hex())
	attempts := client.Database("cpm_dev").Collection("architecture_metrics_attempts_" + bson.NewObjectID().Hex())
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if e := catalog.Drop(cleanup); e != nil {
			t.Error("cannot remove metrics catalog fixture")
		}
		if e := attempts.Drop(cleanup); e != nil {
			t.Error("cannot remove metrics attempts fixture")
		}
		_ = client.Disconnect(cleanup)
	})
	for _, collection := range []*mongo.Collection{catalog, attempts} {
		if err := ensureCollectionOutbox(ctx, collection); err != nil {
			t.Fatal(err)
		}
	}
	_, err = attempts.Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "calculation_status", Value: 1}, {Key: "accepted_at", Value: 1}, {Key: "_id", Value: 1}}, Options: options.Index().SetName("attempts_monitoring_v1").SetPartialFilterExpression(bson.M{"active": true, "status": "accepted"})})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	docs := make([]any, 0, 2005)
	for range 2000 {
		docs = append(docs, bson.M{"active": true, "status": "accepted", "calculation_status": "ready", "accepted_at": now.Add(-48 * time.Hour), "outbox_pending": false})
	}
	for _, item := range []struct {
		status   string
		revision int64
		age      time.Duration
	}{{"queued", 1, 90 * time.Second}, {"queued", 2, 48 * time.Hour}, {"processing", 1, 120 * time.Second}, {"retrying", 1, 60 * time.Second}, {"failed", 1, 300 * time.Second}} {
		docs = append(docs, bson.M{"active": true, "status": "accepted", "calculation_status": item.status, "calculation_revision": item.revision, "accepted_at": now.Add(-item.age), "outbox_pending": item.status == "queued"})
	}
	if _, err = attempts.InsertMany(ctx, docs); err != nil {
		t.Fatal(err)
	}
	if _, err = catalog.InsertMany(ctx, []any{bson.M{"outbox_pending": true}, bson.M{"outbox_pending": false}}); err != nil {
		t.Fatal(err)
	}
	got, err := collectTestsBacklog(ctx, catalog, attempts)
	if err != nil {
		t.Fatal(err)
	}
	if got.Outbox != [2]int64{1, 2} || got.Calculations != [4]int64{2, 1, 1, 1} || !got.OldestInitial.Equal(now.Add(-120*time.Second)) {
		t.Fatal("incorrect indexed backlog or old regrade acceptance age", got)
	}
	var plan bson.M
	err = attempts.Database().RunCommand(ctx, bson.D{{Key: "explain", Value: bson.D{{Key: "find", Value: attempts.Name()}, {Key: "filter", Value: bson.M{"active": true, "status": "accepted", "calculation_status": "queued", "calculation_revision": 1}}, {Key: "sort", Value: bson.D{{Key: "accepted_at", Value: 1}, {Key: "_id", Value: 1}}}, {Key: "hint", Value: "attempts_monitoring_v1"}, {Key: "projection", Value: bson.M{"accepted_at": 1, "_id": 0}}, {Key: "limit", Value: 1}}}, {Key: "verbosity", Value: "executionStats"}}).Decode(&plan)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(plan)
	if strings.Contains(string(raw), `"stage":"COLLSCAN"`) || strings.Contains(string(raw), `"stage":"SORT"`) {
		t.Fatal("metrics oldest query scans history or sorts", string(raw))
	}
	t.Log("metrics indices exclude 2000 completed attempts and old recalculation acceptance dates")
}
