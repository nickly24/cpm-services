package calculate

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestMongoMetricsOnlyReadIndexedWorkingSet(t *testing.T) {
	store := integrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := store.EnsureIndexes(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	documents := make([]any, 0, 2007)
	for range 2000 {
		documents = append(documents, bson.M{"_id": bson.NewObjectID(), "status": "ready", "received_at": now.Add(-48 * time.Hour)})
	}
	for _, item := range []struct {
		kind, status string
		age          time.Duration
	}{{InitialGradeKind, "received", 120 * time.Second}, {InitialGradeKind, "received", 30 * time.Second}, {InitialGradeKind, "received", 10 * time.Second}, {InitialGradeKind, "retrying", 100 * time.Second}, {RegradeKind, "processing", 240 * time.Second}, {ContentKind, "failed", 300 * time.Second}, {"", "failed", 400 * time.Second}} {
		doc := bson.M{"_id": bson.NewObjectID(), "status": item.status, "received_at": now.Add(-item.age)}
		if item.kind != "" {
			doc["processing_kind"] = item.kind
		}
		documents = append(documents, doc)
	}
	if _, err := store.Collection.InsertMany(ctx, documents); err != nil {
		t.Fatal(err)
	}
	rows, err := store.metricsBacklog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int64{}
	for _, row := range rows {
		counts[row.Kind+"/"+row.Status] = row.Count
		if row.Kind == InitialGradeKind && row.Status == "received" && !row.Oldest.Equal(now.Add(-120*time.Second)) {
			t.Fatal("wrong oldest pending time", row)
		}
	}
	for key, want := range map[string]int64{"initial_grade/received": 3, "initial_grade/retrying": 1, "regrade/processing": 1, "content/failed": 1, "unknown/failed": 1} {
		if counts[key] != want {
			t.Fatal("incorrect working-set count", key, counts[key], want)
		}
	}
	var plan bson.M
	err = store.Collection.Database().RunCommand(ctx, bson.D{{Key: "explain", Value: bson.D{{Key: "find", Value: store.Collection.Name()}, {Key: "filter", Value: bson.M{"processing_kind": InitialGradeKind, "status": "received"}}, {Key: "sort", Value: bson.D{{Key: "received_at", Value: 1}, {Key: "_id", Value: 1}}}, {Key: "hint", Value: "inbox_monitoring"}, {Key: "projection", Value: bson.M{"received_at": 1, "_id": 0}}, {Key: "limit", Value: 1}}}, {Key: "verbosity", Value: "executionStats"}}).Decode(&plan)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(plan)
	if strings.Contains(string(raw), `"stage":"COLLSCAN"`) || strings.Contains(string(raw), `"stage":"SORT"`) {
		t.Fatal("metrics oldest query scans history or sorts", string(raw))
	}
	t.Log("metrics indexes bounded oldest query with 2000 finished historical messages")
}
