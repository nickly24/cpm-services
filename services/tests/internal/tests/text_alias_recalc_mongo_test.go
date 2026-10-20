package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Uses its own synthetic database on a dedicated disposable local Mongo only.
func TestTextAliasRecalcMongoAcrossDivisionsAndFences(t *testing.T) {
	uri := os.Getenv("CPM_TEXT_RECALC_TEST_MONGO_URI")
	if uri == "" {
		t.Skip("set dedicated loopback text regrade QA Mongo URI")
	}
	if uri != "mongodb://127.0.0.1:27321" {
		t.Fatal("requires dedicated text regrade QA Mongo")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	client, err := mongo.Connect(options.Client().ApplyURI(uri).SetServerSelectionTimeout(5 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Disconnect(context.Background()) })
	db := client.Database("text_regrade_qa_" + bson.NewObjectID().Hex())
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := db.Drop(cleanup); err != nil {
			t.Error(err)
		}
	})
	store := &Store{Collection: db.Collection(CollectionName)}
	key := strings.Repeat("q", 48)
	s := &Server{Store: store, Config: Config{ServiceKey: key}}
	mux := http.NewServeMux()
	s.internalRoutes(mux)
	call := func(path string, body any, authorized bool, want int) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		if authorized {
			r.Header.Set("X-Service-Key", key)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s status=%d want=%d body=%s", path, w.Code, want, w.Body.String())
		}
		var response struct{ Data map[string]any }
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response.Data
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	tid, beforeID, afterID := bson.NewObjectID(), bson.NewObjectID(), bson.NewObjectID()
	beforeQ := []Question{{QuestionID: 1, Type: "text", Text: "Город", Points: 1, CorrectAnswers: []string{"Питер"}}}
	afterQ := []Question{{QuestionID: 1, Type: "text", Text: "Город", Points: 1, CorrectAnswers: []string{"Питер", "Москва"}}}
	enabled := true
	batch := &ChangeBatch{EventID: "qa-alias-event", BeforeVersionID: beforeID, AfterVersionID: afterID, ChangedAt: now, RecalculateOnChange: &enabled}
	for _, ver := range []ContentVersion{
		{ID: beforeID, TestID: tid, OrganizationID: 1, Questions: beforeQ},
		{ID: afterID, TestID: tid, OrganizationID: 1, Questions: afterQ, Batch: batch},
	} {
		if _, err := db.Collection(ContentCollectionName).InsertOne(ctx, ver); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Collection.InsertOne(ctx, Document{ID: tid, OrganizationID: 1, OwnerDivisionID: 11, Revision: 2, Questions: afterQ, ContentVersionID: afterID, HistoryVersionIDs: []bson.ObjectID{beforeID, afterID}, Placements: []Placement{{DivisionID: 11}, {DivisionID: 12}}}); err != nil {
		t.Fatal(err)
	}
	seed := func(division int64) Attempt {
		sub := bson.NewObjectID()
		accepted := now.Add(-time.Minute)
		return Attempt{ID: bson.NewObjectID(), SubmissionID: &sub, OrganizationID: 1, DivisionID: division, AccountID: 7, TestID: tid, VersionID: beforeID, Active: true, Status: "accepted", Revision: 1, AcceptedAt: &accepted, CalculationRevision: 1, CalculationStatus: "ready", QuestionOrder: []int64{1}, Answers: []SubmittedAnswer{{QuestionID: 1, Value: " Москва "}}, SnapshotHash: "frozen", Result: &GradeResult{MaxPoints: 1, Questions: []QuestionResult{{QuestionID: 1}}}}
	}
	// More than one page; the same account may have independent attempts in both divisions.
	eligible := []Attempt{}
	for i := 0; i < 53; i++ {
		a := seed(11 + int64(i%2))
		if _, err := store.Attempts().InsertOne(ctx, a); err != nil {
			t.Fatal(err)
		}
		eligible = append(eligible, a)
	}
	excluded := []Attempt{}
	for i := 0; i < 6; i++ {
		a := seed(12)
		switch i {
		case 0:
			a.TestID = bson.NewObjectID() // Independent copy, not another placement.
		case 1:
			a.OrganizationID = 2
		case 2:
			a.Practice = true
		case 3:
			a.Active = false
		case 4:
			late := now.Add(time.Second)
			a.AcceptedAt = &late // Late offline acceptance keeps its original version.
		case 5:
			a.Status = "in_progress"
		}
		if _, err := store.Attempts().InsertOne(ctx, a); err != nil {
			t.Fatal(err)
		}
		excluded = append(excluded, a)
	}
	input := PrepareRecalculationsDTO{MessageID: batch.EventID, OrganizationID: 1, TestID: tid, BeforeVersionID: beforeID, AfterVersionID: afterID, NeedsRecalc: true, Reasons: []string{"text_correct_added"}}
	call("/internal/tests/recalculations/prepare", input, false, 401)
	// Respect the setting captured by this edit, even when the policy asks for regrade.
	disabled := false
	if _, err := db.Collection(ContentCollectionName).UpdateByID(ctx, afterID, bson.M{"$set": bson.M{"change_batch.recalculate_on_test_change": disabled}}); err != nil {
		t.Fatal(err)
	}
	out := call("/internal/tests/recalculations/prepare", input, true, 200)
	if out["reason"] != "organization_recalculation_disabled" || out["processed_count"] != float64(0) {
		t.Fatal("disabled organization wasn't skipped", out)
	}
	if _, err := db.Collection(ContentCollectionName).UpdateByID(ctx, afterID, bson.M{"$set": bson.M{"change_batch.recalculate_on_test_change": true}}); err != nil {
		t.Fatal(err)
	}
	store.cache().invalidate(1, tid)
	// Immutable content cache is normally safe; this fixture alone changed a version.
	store.payloadCache = newDataCache()
	processed, pages := 0, 0
	for {
		out = call("/internal/tests/recalculations/prepare", input, true, 200)
		processed += int(out["processed_count"].(float64))
		pages++
		input.Cursor, _ = out["next_cursor"].(string)
		if input.Cursor == "" {
			break
		}
	}
	if processed != 53 || pages != 2 {
		t.Fatalf("lost cross-division page: %d attempts/%d pages", processed, pages)
	}
	input.Cursor = ""
	for {
		out = call("/internal/tests/recalculations/prepare", input, true, 200)
		if out["processed_count"] != float64(0) {
			t.Fatal("replayed content edit duplicated jobs", out)
		}
		input.Cursor, _ = out["next_cursor"].(string)
		if input.Cursor == "" {
			break
		}
	}
	divisions := map[int64]int{}
	for _, old := range eligible {
		a, err := store.attempt(ctx, old.OrganizationID, old.ID)
		if err != nil {
			t.Fatal(err)
		}
		if a.CalculationRevision != 2 || a.CalculationStatus != "queued" || a.Result != nil || a.DivisionID != old.DivisionID || a.SubmissionID == nil || *a.SubmissionID != *old.SubmissionID || a.VersionID != beforeID || a.GradingVersionID != afterID || !reflect.DeepEqual(a.Answers, old.Answers) || a.SnapshotHash != old.SnapshotHash {
			t.Fatal("regrade changed accepted identity/input or lost version/scope")
		}
		job, err := store.currentJob(ctx, a)
		if err != nil || job.DivisionID != old.DivisionID || !reflect.DeepEqual(job.Questions[0].CorrectAnswers, []string{"Питер", "Москва"}) {
			t.Fatal("new job lost accepted answer or division", err)
		}
		result := ResultDTO{CalculationRevision: job.Revision, InputHash: job.InputHash, GradingVersionID: job.GradingVersionID, ExcludedQuestionIDs: job.ExcludedQuestionIDs, GradeResult: GradeResult{Score: 100, EarnedPoints: 1, MaxPoints: 1, Questions: []QuestionResult{{QuestionID: 1, Points: 1, IsCorrect: true}}}}
		path := fmt.Sprintf("/internal/tests/submissions/%s/result", old.SubmissionID.Hex())
		call(path, result, true, 200)
		call(path, result, true, 200)
		a, err = store.attempt(ctx, old.OrganizationID, old.ID)
		if err != nil || a.Result.Score != 100 || len(a.CalculationHistory) != 2 {
			t.Fatal("new result/history wasn't persisted exactly once", err)
		}
		divisions[a.DivisionID]++
	}
	if len(divisions) != 2 {
		t.Fatal("only one division was regraded", divisions)
	}
	for _, old := range excluded {
		a, err := store.attempt(ctx, old.OrganizationID, old.ID)
		if err != nil || !reflect.DeepEqual(a, old) {
			t.Fatal("independent copy, foreign org, practice, reset, late or in-progress attempt changed", err)
		}
	}
}
