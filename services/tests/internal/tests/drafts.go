package tests

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func draftID(r *http.Request) (bson.ObjectID, error) {
	id, e := bson.ObjectIDFromHex(r.PathValue("draft"))
	if e != nil {
		return id, bad("invalid_id")
	}
	return id, nil
}
func (s *Server) loadDraft(r *http.Request, a Actor, d int64) (Draft, error) {
	var out Draft
	id, e := draftID(r)
	if e != nil {
		return out, e
	}
	e = s.Store.Collection.Database().Collection(DraftCollectionName).FindOne(r.Context(), bson.M{"_id": id, "organization_id": a.OrganizationID, "division_id": d}).Decode(&out)
	if errors.Is(e, mongo.ErrNoDocuments) {
		return out, missing
	}
	return out, e
}
func (s *Server) draftList(r *http.Request, a Actor, d int64) (any, int, error) {
	if !a.Can(false) {
		return nil, 0, denied
	}
	limit, offset, e := pagination(r)
	if e != nil {
		return nil, 0, e
	}
	state := r.URL.Query().Get("state")
	if state == "" {
		state = "draft"
	}
	if state != "draft" && state != "publishing" && state != "published" && state != "archived" {
		return nil, 0, bad("invalid_draft_state")
	}
	filter := bson.M{"organization_id": a.OrganizationID, "division_id": d, "state": state}
	coll := s.Store.Collection.Database().Collection(DraftCollectionName)
	total, e := coll.CountDocuments(r.Context(), filter)
	if e != nil {
		return nil, 0, e
	}
	cur, e := coll.Find(r.Context(), filter, options.Find().SetSort(bson.D{{Key: "updated_at", Value: -1}, {Key: "_id", Value: -1}}).SetSkip(offset).SetLimit(limit))
	if e != nil {
		return nil, 0, e
	}
	defer cur.Close(r.Context())
	items := []Draft{}
	if e = cur.All(r.Context(), &items); e != nil {
		return nil, 0, e
	}
	return DraftListView{items, total, limit, offset}, 200, nil
}
func (s *Server) draftGet(r *http.Request, a Actor, d int64) (any, int, error) {
	if !a.Can(false) {
		return nil, 0, denied
	}
	draft, e := s.loadDraft(r, a, d)
	return draft, 200, e
}
func (s *Server) insertDraft(r *http.Request, a Actor, d int64, in DraftDTO, source *bson.ObjectID, payload any) (any, int, error) {
	if e := validateDraft(in); e != nil {
		return nil, 0, e
	}
	id, hash, e := operation(r, a, payload)
	if e != nil {
		return nil, 0, e
	}
	now := time.Now().UTC()
	draft := Draft{ID: id, OrganizationID: a.OrganizationID, DivisionID: d, Revision: 1, Title: strings.TrimSpace(in.Title), Description: strings.TrimSpace(in.Description), DirectionID: in.DirectionID, Questions: normalizeQuestions(in.Questions), Layout: append([]DraftPosition{}, in.Layout...), State: "draft", SourceTestID: source, CreatedBy: a.AccountID, UpdatedBy: a.AccountID, CreatedAt: now, UpdatedAt: now, OperationHash: hash}
	coll := s.Store.Collection.Database().Collection(DraftCollectionName)
	_, e = coll.InsertOne(r.Context(), draft)
	if mongo.IsDuplicateKeyError(e) {
		var old Draft
		e = coll.FindOne(r.Context(), bson.M{"_id": id, "organization_id": a.OrganizationID, "division_id": d}).Decode(&old)
		if errors.Is(e, mongo.ErrNoDocuments) {
			return nil, 0, conflict("idempotency_conflict")
		}
		if e != nil {
			return nil, 0, e
		}
		if old.OperationHash != hash {
			return nil, 0, conflict("idempotency_conflict")
		}
		return old, 200, nil
	}
	return draft, 201, e
}
func (s *Server) draftCreate(r *http.Request, a Actor, d int64) (any, int, error) {
	if !a.Can(true) {
		return nil, 0, denied
	}
	var in DraftDTO
	if e := decode(r, &in); e != nil {
		return nil, 0, e
	}
	return s.insertDraft(r, a, d, in, nil, in)
}
func (s *Server) draftFromTest(r *http.Request, a Actor, d int64) (any, int, error) {
	if !a.Can(true) {
		return nil, 0, denied
	}
	var in struct{}
	if e := decode(r, &in); e != nil {
		return nil, 0, e
	}
	id, hash, e := operation(r, a, in)
	if e != nil {
		return nil, 0, e
	}
	var existing Draft
	e = s.Store.Collection.Database().Collection(DraftCollectionName).FindOne(r.Context(), bson.M{"_id": id, "organization_id": a.OrganizationID}).Decode(&existing)
	if e == nil {
		if existing.OperationHash != hash || existing.DivisionID != d {
			return nil, 0, conflict("idempotency_conflict")
		}
		return existing, 200, nil
	}
	if !errors.Is(e, mongo.ErrNoDocuments) {
		return nil, 0, e
	}
	test, e := s.load(r, a)
	if e != nil {
		return nil, 0, e
	}
	if !test.Readable(a) {
		return nil, 0, missing
	}
	if test.Archived {
		return nil, 0, conflict("test_archived")
	}
	return s.insertDraft(r, a, d, DraftDTO{test.Title, test.Description, test.DirectionID, test.Questions, []DraftPosition{}}, &test.ID, in)
}
func draftWritable(d Draft, a Actor) error {
	if d.State != "draft" {
		return conflict("draft_not_editable")
	}
	if d.Lock == nil || d.Lock.ActorID != a.AccountID || !d.Lock.ExpiresAt.After(time.Now().UTC()) {
		return conflict("draft_lock_required")
	}
	return nil
}
func (s *Server) updateDraft(r *http.Request, a Actor, draft Draft, revision int64, fields bson.M, requireLock bool) (Draft, error) {
	filter := bson.M{"_id": draft.ID, "organization_id": a.OrganizationID, "division_id": draft.DivisionID, "revision": revision}
	if requireLock {
		filter["state"] = "draft"
		filter["lock.actor_id"] = a.AccountID
		filter["lock.expires_at"] = bson.M{"$gt": time.Now().UTC()}
	}
	fields["revision"] = revision + 1
	fields["updated_by"] = a.AccountID
	fields["updated_at"] = time.Now().UTC()
	var out Draft
	e := s.Store.Collection.Database().Collection(DraftCollectionName).FindOneAndUpdate(r.Context(), filter, bson.M{"$set": fields}, options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&out)
	if errors.Is(e, mongo.ErrNoDocuments) {
		return out, conflict("stale_revision_or_lock")
	}
	return out, e
}
func (s *Server) draftEdit(r *http.Request, a Actor, d int64) (any, int, error) {
	if !a.Can(true) {
		return nil, 0, denied
	}
	draft, e := s.loadDraft(r, a, d)
	if e != nil {
		return nil, 0, e
	}
	var in DraftEditDTO
	if e = decode(r, &in); e != nil {
		return nil, 0, e
	}
	if e = checkRevision(draft.Revision, in.ExpectedRevision); e != nil {
		return nil, 0, e
	}
	if e = draftWritable(draft, a); e != nil {
		return nil, 0, e
	}
	if e = validateDraft(in.DraftDTO); e != nil {
		return nil, 0, e
	}
	fields := bson.M{"title": strings.TrimSpace(in.Title), "description": strings.TrimSpace(in.Description), "direction_id": in.DirectionID, "questions": normalizeQuestions(in.Questions), "layout": append([]DraftPosition{}, in.Layout...)}
	draft, e = s.updateDraft(r, a, draft, in.ExpectedRevision, fields, true)
	return draft, 200, e
}
func (s *Server) draftLock(r *http.Request, a Actor, d int64) (any, int, error) {
	if !a.Can(true) {
		return nil, 0, denied
	}
	draft, e := s.loadDraft(r, a, d)
	if e != nil {
		return nil, 0, e
	}
	var in RevisionDTO
	if e = decode(r, &in); e != nil {
		return nil, 0, e
	}
	if e = checkRevision(draft.Revision, in.ExpectedRevision); e != nil {
		return nil, 0, e
	}
	if draft.State != "draft" {
		return nil, 0, conflict("draft_not_editable")
	}
	if draft.Lock != nil && draft.Lock.ActorID != a.AccountID && draft.Lock.ExpiresAt.After(time.Now().UTC()) {
		return nil, 0, conflict("draft_locked")
	}
	draft, e = s.updateDraft(r, a, draft, in.ExpectedRevision, bson.M{"lock": DraftLock{a.AccountID, time.Now().UTC().Add(90 * time.Second)}}, false)
	return draft, 200, e
}
func (s *Server) draftUnlock(r *http.Request, a Actor, d int64) (any, int, error) {
	if !a.Can(true) {
		return nil, 0, denied
	}
	draft, e := s.loadDraft(r, a, d)
	if e != nil {
		return nil, 0, e
	}
	var in RevisionDTO
	if e = decode(r, &in); e != nil {
		return nil, 0, e
	}
	if e = checkRevision(draft.Revision, in.ExpectedRevision); e != nil {
		return nil, 0, e
	}
	if draft.State != "draft" {
		return nil, 0, conflict("draft_not_editable")
	}
	if draft.Lock != nil && draft.Lock.ActorID != a.AccountID {
		return nil, 0, conflict("draft_locked")
	}
	draft, e = s.updateDraft(r, a, draft, in.ExpectedRevision, bson.M{"lock": nil}, false)
	return draft, 200, e
}
func (s *Server) draftDelete(r *http.Request, a Actor, d int64) (any, int, error) {
	if !a.Can(true) {
		return nil, 0, denied
	}
	draft, e := s.loadDraft(r, a, d)
	if e != nil {
		return nil, 0, e
	}
	var in RevisionDTO
	if e = decode(r, &in); e != nil {
		return nil, 0, e
	}
	if e = checkRevision(draft.Revision, in.ExpectedRevision); e != nil {
		return nil, 0, e
	}
	if draft.State != "draft" {
		return nil, 0, conflict("draft_not_editable")
	}
	if draft.Lock != nil && draft.Lock.ActorID != a.AccountID && draft.Lock.ExpiresAt.After(time.Now().UTC()) {
		return nil, 0, conflict("draft_locked")
	}
	draft, e = s.updateDraft(r, a, draft, in.ExpectedRevision, bson.M{"state": "archived", "lock": nil}, false)
	return draft, 200, e
}
func publicationID(id bson.ObjectID) bson.ObjectID {
	sum := sha256.Sum256(append([]byte("tests-draft-publication-v1:"), id[:]...))
	var out bson.ObjectID
	copy(out[:], sum[:12])
	return out
}
func (s *Server) finishPublication(r *http.Request, a Actor, draft Draft) (any, int, error) {
	id := *draft.PublishedTestID
	sealed := DraftDTO{draft.Title, draft.Description, draft.DirectionID, draft.Questions, draft.Layout}
	raw, _ := json.Marshal([]any{draft.ID, draft.PublicationSourceRevision, sealed})
	if draft.LegacyPublicationSettings != nil {
		raw, _ = json.Marshal([]any{draft.ID, draft.PublicationSourceRevision, sealed, draft.LegacyPublicationSettings})
	}
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	doc, e := s.Store.Get(r.Context(), a.OrganizationID, id)
	if errors.Is(e, missing) {
		now := *draft.PublicationStartedAt
		doc = Document{ID: id, OrganizationID: a.OrganizationID, OwnerDivisionID: draft.DivisionID, DirectionID: draft.DirectionID, Title: draft.Title, Description: draft.Description, Revision: 1, Placements: []Placement{{DivisionID: draft.DivisionID, AddedBy: draft.PublisherID, AddedAt: now, UpdatedBy: draft.PublisherID, UpdatedAt: now}}, CreatedBy: draft.PublisherID, UpdatedBy: draft.PublisherID, CreatedAt: now, UpdatedAt: now, OperationHash: hash}
		if draft.LegacyPublicationSettings != nil {
			doc.Placements[0].Settings = *draft.LegacyPublicationSettings
		}
		if e = s.initialContent(r.Context(), &doc, draft.Questions, Actor{AccountID: draft.PublisherID}); e != nil {
			return nil, 0, e
		}
		doc, _, e = s.Store.Insert(r.Context(), doc)
	}
	if e != nil {
		return nil, 0, e
	}
	if doc.OperationHash != hash {
		return nil, 0, conflict("idempotency_conflict")
	}
	if draft.State == "publishing" {
		_, e = s.Store.Collection.Database().Collection(DraftCollectionName).UpdateOne(r.Context(), bson.M{"_id": draft.ID, "organization_id": a.OrganizationID, "state": "publishing", "publication_source_revision": draft.PublicationSourceRevision}, bson.M{"$set": bson.M{"state": "published", "lock": nil, "updated_at": time.Now().UTC()}, "$inc": bson.M{"revision": 1}})
		if e != nil {
			return nil, 0, e
		}
	}
	return s.mutationResult(r, a, doc, draft.DivisionID, 201)
}
func (s *Server) draftPublish(r *http.Request, a Actor, d int64) (any, int, error) {
	if !a.Can(true) {
		return nil, 0, denied
	}
	draft, e := s.loadDraft(r, a, d)
	if e != nil {
		return nil, 0, e
	}
	var in RevisionDTO
	if e = decode(r, &in); e != nil {
		return nil, 0, e
	}
	if draft.State == "publishing" || draft.State == "published" {
		if in.ExpectedRevision != draft.PublicationSourceRevision {
			return nil, 0, conflict("stale_revision")
		}
		body, code, e := s.finishPublication(r, a, draft)
		if e == nil && draft.State == "published" {
			code = 200
		}
		return body, code, e
	}
	if e = checkRevision(draft.Revision, in.ExpectedRevision); e != nil {
		return nil, 0, e
	}
	if e = draftWritable(draft, a); e != nil {
		return nil, 0, e
	}
	if e = validateMetadata(CreateDTO{Title: draft.Title, Description: draft.Description, DirectionID: draft.DirectionID}); e != nil {
		return nil, 0, e
	}
	if e = validateQuestions(draft.Questions, false); e != nil {
		return nil, 0, e
	}
	if e = s.activeReferences(r, a, draft.DirectionID, d); e != nil {
		return nil, 0, e
	}
	id := publicationID(draft.ID)
	now := time.Now().UTC()
	draft, e = s.updateDraft(r, a, draft, in.ExpectedRevision, bson.M{"state": "publishing", "published_test_id": id, "publication_source_revision": in.ExpectedRevision, "publisher_id": a.AccountID, "publication_started_at": now}, true)
	if e != nil {
		return nil, 0, e
	}
	return s.finishPublication(r, a, draft)
}
func (s *Server) draftSpecs() []routeSpec {
	return []routeSpec{
		{"GET /v1/tests/divisions/{division}/drafts", "Черновики выбранного подразделения с пагинацией", s.draftList, nil, DraftListView{}},
		{"POST /v1/tests/divisions/{division}/drafts", "Создать черновик; обязательный Idempotency-Key", s.draftCreate, DraftDTO{}, Draft{}},
		{"GET /v1/tests/divisions/{division}/drafts/{draft}", "Получить черновик и состояние блокировки", s.draftGet, nil, Draft{}},
		{"PUT /v1/tests/divisions/{division}/drafts/{draft}", "Сохранить черновик под своей действующей блокировкой", s.draftEdit, DraftEditDTO{}, Draft{}},
		{"DELETE /v1/tests/divisions/{division}/drafts/{draft}", "Архивировать черновик", s.draftDelete, RevisionDTO{}, Draft{}},
		{"POST /v1/tests/divisions/{division}/tests/{test}/draft", "Создать черновик независимой копии существующего теста", s.draftFromTest, struct{}{}, Draft{}},
		{"POST /v1/tests/divisions/{division}/drafts/{draft}/lock", "Получить или продлить блокировку на 90 секунд", s.draftLock, RevisionDTO{}, Draft{}},
		{"DELETE /v1/tests/divisions/{division}/drafts/{draft}/lock", "Снять свою блокировку", s.draftUnlock, RevisionDTO{}, Draft{}},
		{"POST /v1/tests/divisions/{division}/drafts/{draft}/publish", "Опубликовать черновик как новый самостоятельный скрытый тест", s.draftPublish, RevisionDTO{}, TestView{}},
	}
}
