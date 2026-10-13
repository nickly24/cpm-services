package tests

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func (s *Store) LoadContent(ctx context.Context, org int64, id bson.ObjectID) (ContentVersion, error) {
	cache := s.cache()
	epoch := cache.epoch()
	key := contentCacheKey(org, id)
	if raw, found := cache.get(key); found {
		var cached ContentVersion
		if bson.Unmarshal(raw, &cached) == nil && cached.ID == id && cached.OrganizationID == org {
			return cached, nil
		}
	}
	var v ContentVersion
	e := s.Collection.Database().Collection(ContentCollectionName).FindOne(ctx, bson.M{"_id": id, "organization_id": org}).Decode(&v)
	if errors.Is(e, mongo.ErrNoDocuments) {
		return v, missing
	}
	if e == nil && v.ID == id && v.OrganizationID == org {
		if raw, err := bson.Marshal(v); err == nil {
			cache.put(key, raw, contentTTL, epoch)
		}
	}
	return v, e
}
func (s *Server) initialContent(ctx context.Context, doc *Document, qs []Question, a Actor) error {
	if e := validateQuestions(qs, true); e != nil {
		return e
	}
	doc.Questions = normalizeQuestions(qs)
	max, used, e := transitionIDs(Document{}, doc.Questions)
	if e != nil {
		return e
	}
	doc.MaxQuestionID = max
	doc.UsedOptionIDs = used
	v := ContentVersion{ID: bson.NewObjectID(), TestID: doc.ID, OrganizationID: doc.OrganizationID, Title: doc.Title, Description: doc.Description, DirectionID: doc.DirectionID, Questions: doc.Questions, CreatedAt: doc.CreatedAt, ActorID: a.AccountID}
	if _, e = s.Store.Collection.Database().Collection(ContentCollectionName).InsertOne(ctx, v); e != nil {
		return e
	}
	doc.ContentVersionID = v.ID
	doc.HistoryVersionIDs = []bson.ObjectID{v.ID}
	return nil
}

// The catalog CAS commits the version pointer, history membership and outbox together.
// A failed CAS may leave an immutable orphan, which history never exposes.
func (s *Server) saveContent(ctx context.Context, old, next Document, expected int64, a Actor) (Document, error) {
	if reflect.DeepEqual(snapshot(old), snapshot(next)) {
		return old, nil
	}
	organizationSettings, e := s.Dependencies.OrganizationTestSettings(ctx, old.OrganizationID)
	if e != nil {
		return old, e
	}
	max, used, e := transitionIDs(old, next.Questions)
	if e != nil {
		return old, e
	}
	beforeVersion := old.ContentVersionID
	// Existing metadata-only records receive a baseline before their first edit.
	if beforeVersion.IsZero() {
		if e = s.initialContent(ctx, &old, old.Questions, a); e != nil {
			return old, e
		}
		beforeVersion = old.ContentVersionID
	}
	now := time.Now().UTC()
	versionID := bson.NewObjectID()
	batch := ChangeBatch{BatchID: versionID, TestID: next.ID, OrganizationID: next.OrganizationID, OwnerDivisionID: next.OwnerDivisionID, ActorID: a.AccountID, ActorRole: a.Role, ChangedAt: now, BeforeVersionID: beforeVersion, AfterVersionID: versionID, Before: snapshot(old), After: snapshot(next), Changes: contentChanges(snapshot(old), snapshot(next))}
	batch.EventID = uuid()
	batch.RecalculateOnChange = organizationSettings.RecalculateOnChange
	batch.OrganizationSettingsRevision = organizationSettings.Revision
	version := ContentVersion{ID: versionID, TestID: next.ID, OrganizationID: next.OrganizationID, Title: next.Title, Description: next.Description, DirectionID: next.DirectionID, Questions: next.Questions, CreatedAt: now, ActorID: a.AccountID, Batch: &batch}
	if _, e = s.Store.Collection.Database().Collection(ContentCollectionName).InsertOne(ctx, version); e != nil {
		return old, e
	}
	next.MaxQuestionID = max
	next.UsedOptionIDs = used
	next.ContentVersionID = versionID
	next.HistoryVersionIDs = append(append([]bson.ObjectID{}, old.HistoryVersionIDs...), versionID)
	event := NewEvent("tests.content.changed", map[string]any{"test_id": next.ID, "organization_id": next.OrganizationID, "division_id": next.OwnerDivisionID, "before_version_id": beforeVersion, "after_version_id": versionID, "change_batch": batch})
	event.MessageID = batch.EventID
	event.CorrelationID = batch.EventID
	return s.Store.EnqueueSave(ctx, next, expected, a.AccountID, event)
}
func (s *Server) contentGet(r *http.Request, a Actor, _ int64) (any, int, error) {
	doc, e := s.load(r, a)
	if e != nil {
		return nil, 0, e
	}
	if !doc.Readable(a) {
		return nil, 0, missing
	}
	return contentView(doc), 200, nil
}
func (s *Server) contentPut(r *http.Request, a Actor, _ int64) (any, int, error) {
	doc, e := s.load(r, a)
	if e != nil {
		return nil, 0, e
	}
	if !doc.Editable(a) {
		return nil, 0, denied
	}
	if doc.Archived {
		return nil, 0, conflict("test_archived")
	}
	var in ContentDTO
	if e = decode(r, &in); e != nil {
		return nil, 0, e
	}
	if e = checkRevision(doc.Revision, in.ExpectedRevision); e != nil {
		return nil, 0, e
	}
	meta := CreateDTO{Title: in.Title, Description: in.Description, DirectionID: in.DirectionID}
	if e = validateMetadata(meta); e != nil {
		return nil, 0, e
	}
	if e = validateQuestions(in.Questions, true); e != nil {
		return nil, 0, e
	}
	if len(in.Questions) == 0 {
		for _, p := range doc.Placements {
			if p.Settings.Published {
				return nil, 0, conflict("published_test_requires_questions")
			}
		}
	}
	if e = s.activeReferences(r, a, in.DirectionID, doc.OwnerDivisionID); e != nil {
		return nil, 0, e
	}
	next := doc
	next.Title = strings.TrimSpace(in.Title)
	next.Description = strings.TrimSpace(in.Description)
	next.DirectionID = in.DirectionID
	next.Questions = normalizeQuestions(in.Questions)
	next, e = s.saveContent(r.Context(), doc, next, in.ExpectedRevision, a)
	if e != nil {
		return nil, 0, e
	}
	return contentView(next), 200, nil
}
func (s *Server) contentHistory(r *http.Request, a Actor, _ int64) (any, int, error) {
	doc, e := s.load(r, a)
	if e != nil {
		return nil, 0, e
	}
	if !doc.Readable(a) {
		return nil, 0, missing
	}
	limit, offset, e := pagination(r)
	if e != nil {
		return nil, 0, e
	}
	items := []ContentVersion{}
	if len(doc.HistoryVersionIDs) > 0 {
		cur, e := s.Store.Collection.Database().Collection(ContentCollectionName).Find(r.Context(), bson.M{"organization_id": a.OrganizationID, "test_id": doc.ID, "_id": bson.M{"$in": doc.HistoryVersionIDs}}, options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}, {Key: "_id", Value: -1}}).SetSkip(offset).SetLimit(limit))
		if e != nil {
			return nil, 0, e
		}
		defer cur.Close(r.Context())
		if e = cur.All(r.Context(), &items); e != nil {
			return nil, 0, e
		}
	}
	return HistoryView{items, int64(len(doc.HistoryVersionIDs)), limit, offset}, 200, nil
}
func (s *Server) contentSpecs() []routeSpec {
	return []routeSpec{
		{"GET /v1/tests/{test}/content", "Содержимое и ключи для администраторов, имеющих доступ к тесту", s.contentGet, nil, ContentView{}},
		{"PUT /v1/tests/{test}/content", "Сохранить весь редактор одной версией и одним событием изменений", s.contentPut, ContentDTO{}, ContentView{}},
		{"GET /v1/tests/{test}/history", "История опубликованных сохранений с полными значениями до/после", s.contentHistory, nil, HistoryView{}},
	}
}
