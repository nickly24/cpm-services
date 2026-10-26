package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Opt-in persistence test against a disposable, loopback-only QA Mongo.
func TestLargeQuestionImportMongoCommitReplayAndReadback(t *testing.T) {
	uri := os.Getenv("CPM_LARGE_IMPORT_MONGO_URI")
	if uri == "" {
		t.Skip("disposable local Mongo not configured")
	}
	if uri != "mongodb://127.0.0.1:27336" {
		t.Fatal("requires dedicated loopback QA Mongo")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	client, err := mongo.Connect(options.Client().ApplyURI(uri).SetServerSelectionTimeout(5 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Disconnect(context.Background())
	db := client.Database("large_question_qa_" + bson.NewObjectID().Hex())
	defer db.Drop(context.Background())
	s := &Server{Store: &Store{Collection: db.Collection(CollectionName)}, Dependencies: templateTestDependencies(t)}
	in := largeQuestionTable(t, 6000)
	preview := buildJSONImportPreview(in, "Математика")
	if !preview.Valid {
		t.Fatal(preview.Errors)
	}
	command := JSONImportCommitDTO{JSONImportPreviewDTO: in, CommandID: "c1260000-0000-4000-8000-000000000001", PreviewHash: preview.PreviewHash}
	call := func(method, path string, body any, code int) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer admin")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != code {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		return w
	}
	created := call("POST", "/v1/tests/divisions/17/imports/json/commit", command, 201)
	var result struct {
		Data JSONImportCommitResult `json:"data"`
	}
	if json.Unmarshal(created.Body.Bytes(), &result) != nil || result.Data.Test.QuestionCount != 6000 {
		t.Fatal("wrong committed question count")
	}
	id := result.Data.Test.ID
	call("POST", "/v1/tests/divisions/17/imports/json/commit", command, 200)
	if n, e := db.Collection(CollectionName).CountDocuments(ctx, bson.M{}); e != nil || n != 1 {
		t.Fatalf("replay created duplicate: %d %v", n, e)
	}
	if n, e := db.Collection(ContentCollectionName).CountDocuments(ctx, bson.M{}); e != nil || n != 1 {
		t.Fatalf("replay created version: %d %v", n, e)
	}
	doc, e := s.Store.Get(ctx, 11, id)
	if e != nil {
		t.Fatal(e)
	}
	version, e := s.Store.LoadContent(ctx, 11, doc.ContentVersionID)
	if e != nil || len(version.Questions) != 6000 {
		t.Fatalf("version truncated: %v", e)
	}
	if doc.Questions[5999].QuestionID != 6000 || len(doc.Questions[5999].CorrectAnswers) != 3 {
		t.Fatal("last question lost")
	}
	read := call("GET", "/v1/tests/"+id.Hex()+"/content", nil, 200)
	var content struct {
		Data ContentView `json:"data"`
	}
	if json.Unmarshal(read.Body.Bytes(), &content) != nil || len(content.Data.Questions) != 6000 {
		t.Fatal("HTTP readback truncated")
	}
	bad := command
	bad.PreviewHash = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	call("POST", "/v1/tests/divisions/17/imports/json/commit", bad, 409)
	// Editing a large imported test must persist its history and full change event.
	edit := ContentDTO{ExpectedRevision: doc.Revision, Title: doc.Title + " обновлён", DirectionID: doc.DirectionID, Questions: doc.Questions}
	call("PUT", "/v1/tests/"+id.Hex()+"/content", edit, 200)
	updated, err := s.Store.Get(ctx, 11, id)
	if err != nil || updated.Revision != doc.Revision+1 || len(updated.Questions) != 6000 || len(updated.HistoryVersionIDs) != 2 {
		t.Fatalf("large edit/history failed: %v", err)
	}
	// Reusing content/IDs must also work for the student-safe projection.
	if len(SafeQuestions(version.Questions)) != 6000 {
		t.Fatal("student projection truncated")
	}
}
