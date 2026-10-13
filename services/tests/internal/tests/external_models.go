package tests

import (
	"context"
	"errors"
	"reflect"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const ExternalCollection = "tests_external_catalog_v1"
const ExternalVersionsCollection = "tests_external_versions_v1"
const ExternalDomainCollection = "tests_external_domain_v1"

// All current results and durable command receipts share one aggregate. A CAS
// therefore confirms the entire import without needing Mongo transactions.
type ExternalTestDocument struct {
	ID             bson.ObjectID            `json:"id" bson:"_id"`
	OrganizationID int64                    `json:"organization_id" bson:"organization_id"`
	DivisionID     int64                    `json:"division_id" bson:"division_id"`
	Name           string                   `json:"name" bson:"name"`
	DirectionID    int64                    `json:"direction_id" bson:"direction_id"`
	Date           string                   `json:"date" bson:"date"`
	Revision       int64                    `json:"revision" bson:"revision"`
	Deleted        bool                     `json:"is_deleted" bson:"is_deleted"`
	Results        []ExternalResult         `json:"-" bson:"results"`
	HistoryIDs     []bson.ObjectID          `json:"-" bson:"history_ids"`
	Commands       []ExternalCommandReceipt `json:"-" bson:"commands"`
	OperationHash  string                   `json:"-" bson:"operation_hash"`
	CreatedBy      int64                    `json:"created_by" bson:"created_by"`
	UpdatedBy      int64                    `json:"updated_by" bson:"updated_by"`
	CreatedAt      time.Time                `json:"created_at" bson:"created_at"`
	UpdatedAt      time.Time                `json:"updated_at" bson:"updated_at"`
}
type ExternalResult struct {
	ID           bson.ObjectID `json:"id" bson:"_id"`
	Revision     int64         `json:"revision" bson:"revision"`
	AccountID    int64         `json:"account_id" bson:"account_id"`
	StudentID    int64         `json:"student_id" bson:"student_id"`
	FullName     string        `json:"full_name" bson:"full_name"`
	Score        *float64      `json:"score" bson:"score"`
	CorrectCount *int64        `json:"correct_count,omitempty" bson:"correct_count,omitempty"`
	CompletedAt  string        `json:"completed_at,omitempty" bson:"completed_at,omitempty"`
	TimeSpent    string        `json:"time_spent,omitempty" bson:"time_spent,omitempty"`
	Login        string        `json:"login,omitempty" bson:"login,omitempty"`
	SourceRowID  string        `json:"source_row_id,omitempty" bson:"source_row_id,omitempty"`
}
type ExternalResultReceipt struct {
	RowID     string        `json:"row_id" bson:"row_id"`
	ResultID  bson.ObjectID `json:"result_id" bson:"result_id"`
	AccountID int64         `json:"account_id" bson:"account_id"`
	Revision  int64         `json:"revision" bson:"revision"`
	Percent   float64       `json:"percent" bson:"percent"`
}
type ExternalCommandReceipt struct {
	CommandID             string                  `json:"command_id" bson:"command_id"`
	InputHash             string                  `json:"input_hash" bson:"input_hash"`
	ActorID               int64                   `json:"actor_id" bson:"actor_id"`
	SourceRevision        int64                   `json:"source_revision" bson:"source_revision"`
	Results               []ExternalResultReceipt `json:"results" bson:"results"`
	UploadID              string                  `json:"upload_id,omitempty" bson:"upload_id,omitempty"`
	UploadSessionID       string                  `json:"upload_session_id,omitempty" bson:"upload_session_id,omitempty"`
	UploadPreviewRevision int64                   `json:"upload_preview_revision,omitempty" bson:"upload_preview_revision,omitempty"`
}
type ExternalMetadata struct {
	Name        string `json:"name" bson:"name"`
	DirectionID int64  `json:"direction_id" bson:"direction_id"`
	Date        string `json:"date" bson:"date"`
	Deleted     bool   `json:"is_deleted" bson:"is_deleted"`
}
type ExternalResultChange struct {
	AccountID int64           `json:"account_id" bson:"account_id"`
	Before    *ExternalResult `json:"before" bson:"before"`
	After     *ExternalResult `json:"after" bson:"after"`
}
type ExternalHistory struct {
	ID             bson.ObjectID          `json:"id" bson:"_id"`
	TestID         bson.ObjectID          `json:"test_id" bson:"test_id"`
	OrganizationID int64                  `json:"organization_id" bson:"organization_id"`
	DivisionID     int64                  `json:"division_id" bson:"division_id"`
	Revision       int64                  `json:"revision" bson:"revision"`
	Action         string                 `json:"action" bson:"action"`
	ActorID        int64                  `json:"actor_id" bson:"actor_id"`
	ChangedAt      time.Time              `json:"changed_at" bson:"changed_at"`
	Before         *ExternalMetadata      `json:"metadata_before" bson:"metadata_before"`
	After          ExternalMetadata       `json:"metadata_after" bson:"metadata_after"`
	Changes        []ExternalResultChange `json:"result_changes" bson:"result_changes"`
}
type ExternalActions struct {
	CanEdit        bool `json:"can_edit"`
	CanDelete      bool `json:"can_delete"`
	CanViewResults bool `json:"can_view_results"`
}
type ExternalTestView struct {
	ID             bson.ObjectID   `json:"id"`
	Revision       int64           `json:"revision"`
	OrganizationID int64           `json:"organization_id"`
	DivisionID     int64           `json:"division_id"`
	Name           string          `json:"name"`
	DirectionID    int64           `json:"direction_id"`
	DirectionName  string          `json:"direction_name"`
	Date           string          `json:"date"`
	Deleted        bool            `json:"is_deleted"`
	Actions        ExternalActions `json:"actions"`
	Result         *ExternalResult `json:"result"`
}
type ExternalResultListView struct {
	Items  []ExternalResult `json:"items"`
	Total  int64            `json:"total"`
	Limit  int64            `json:"limit"`
	Offset int64            `json:"offset"`
}
type ExternalHistoryListView struct {
	Items  []ExternalHistory `json:"items"`
	Total  int64             `json:"total"`
	Limit  int64             `json:"limit"`
	Offset int64             `json:"offset"`
}
type ExternalFlatView struct {
	Test   ExternalTestView `json:"test"`
	Result ExternalResult   `json:"result"`
}
type ExternalFlatListView struct {
	Items  []ExternalFlatView `json:"items"`
	Total  int64              `json:"total"`
	Limit  int64              `json:"limit"`
	Offset int64              `json:"offset"`
}
type ExternalDeletedView struct {
	TestID         bson.ObjectID `json:"test_id"`
	ResultsDeleted int           `json:"results_deleted"`
}

func (d ExternalTestDocument) result(account int64) *ExternalResult {
	for i := range d.Results {
		if d.Results[i].AccountID == account {
			r := d.Results[i]
			return &r
		}
	}
	return nil
}
func (d ExternalTestDocument) metadata() ExternalMetadata {
	return ExternalMetadata{d.Name, d.DirectionID, d.Date, d.Deleted}
}
func (s *Store) External() *mongo.Collection {
	return s.Collection.Database().Collection(ExternalCollection)
}
func (s *Store) ExternalVersions() *mongo.Collection {
	return s.Collection.Database().Collection(ExternalVersionsCollection)
}
func (s *Store) ExternalGet(ctx context.Context, org int64, id bson.ObjectID) (ExternalTestDocument, error) {
	var d ExternalTestDocument
	e := s.External().FindOne(ctx, bson.M{"_id": id, "organization_id": org}).Decode(&d)
	if errors.Is(e, mongo.ErrNoDocuments) {
		e = missing
	}
	return d, e
}
func (s *Store) EnsureExternalIndexes(ctx context.Context) error {
	_, e := s.External().Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "organization_id", Value: 1}, {Key: "division_id", Value: 1}, {Key: "is_deleted", Value: 1}, {Key: "date", Value: -1}, {Key: "_id", Value: -1}}, Options: options.Index().SetName("external_division_catalog")},
		{Keys: bson.D{{Key: "organization_id", Value: 1}, {Key: "division_id", Value: 1}, {Key: "direction_id", Value: 1}, {Key: "date", Value: 1}}, Options: options.Index().SetName("external_rating_period")},
	})
	if e != nil {
		return e
	}
	_, e = s.ExternalVersions().Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "organization_id", Value: 1}, {Key: "division_id", Value: 1}, {Key: "test_id", Value: 1}, {Key: "revision", Value: -1}}, Options: options.Index().SetName("external_history")})
	if e != nil {
		return e
	}
	// This executable registers the real domain routes before advertising ready.
	_, e = s.Collection.Database().Collection(ExternalDomainCollection).UpdateOne(ctx, bson.M{"_id": "external-tests"}, bson.M{"$set": bson.M{"schema_version": 1, "status": "ready"}}, options.UpdateOne().SetUpsert(true))
	return e
}
func externalChanges(before, after []ExternalResult) []ExternalResultChange {
	out := []ExternalResultChange{}
	old := map[int64]ExternalResult{}
	next := map[int64]ExternalResult{}
	for _, r := range before {
		old[r.AccountID] = r
	}
	for _, r := range after {
		next[r.AccountID] = r
	}
	for _, r := range before {
		n, ok := next[r.AccountID]
		if !ok {
			v := r
			out = append(out, ExternalResultChange{AccountID: r.AccountID, Before: &v})
		} else if !reflect.DeepEqual(r, n) {
			v, w := r, n
			out = append(out, ExternalResultChange{r.AccountID, &v, &w})
		}
	}
	for _, r := range after {
		if _, ok := old[r.AccountID]; !ok {
			v := r
			out = append(out, ExternalResultChange{AccountID: r.AccountID, After: &v})
		}
	}
	return out
}
func (s *Server) externalSave(ctx context.Context, before, after ExternalTestDocument, actor Actor, action string) (ExternalTestDocument, error) {
	if before.ID != after.ID || before.OrganizationID != after.OrganizationID || before.DivisionID != after.DivisionID || actor.OrganizationID != after.OrganizationID || !actor.InDivision(after.DivisionID) {
		return after, denied
	}
	if len(after.Results) > 2000 {
		return after, &APIError{413, "external_results_too_large"}
	}
	seen := map[int64]bool{}
	for _, row := range after.Results {
		if row.ID.IsZero() || row.AccountID < 1 || row.StudentID < 1 || row.Revision < 1 || seen[row.AccountID] || !validExternalScore(row.Score) {
			return after, bad("invalid_external_results")
		}
		seen[row.AccountID] = true
	}
	after.Revision = before.Revision + 1
	after.UpdatedBy = actor.AccountID
	after.UpdatedAt = time.Now().UTC().Truncate(time.Millisecond)
	m := before.metadata()
	h := ExternalHistory{ID: bson.NewObjectID(), TestID: after.ID, OrganizationID: after.OrganizationID, DivisionID: after.DivisionID, Revision: after.Revision, Action: action, ActorID: actor.AccountID, ChangedAt: after.UpdatedAt, Before: &m, After: after.metadata(), Changes: externalChanges(before.Results, after.Results)}
	if _, e := s.Store.ExternalVersions().InsertOne(ctx, h); e != nil {
		return after, e
	}
	after.HistoryIDs = append(append([]bson.ObjectID{}, before.HistoryIDs...), h.ID)
	if raw, e := bson.Marshal(after); e != nil {
		return after, e
	} else if len(raw) > 15<<20 {
		return after, &APIError{413, "external_aggregate_too_large"}
	}
	fields, e := documentFields(after)
	if e != nil {
		return after, e
	}
	r, e := s.Store.External().UpdateOne(ctx, bson.M{"_id": before.ID, "organization_id": before.OrganizationID, "division_id": before.DivisionID, "revision": before.Revision, "is_deleted": before.Deleted}, bson.M{"$set": fields})
	if e == nil && r.MatchedCount != 1 {
		e = conflict("stale_revision")
	}
	return after, e
}
