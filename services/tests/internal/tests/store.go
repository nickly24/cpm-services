package tests

import (
	"context"
	"errors"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"reflect"
	"sync"
	"time"
)

const CollectionName = "tests_catalog_v1"

type Store struct {
	Collection   *mongo.Collection
	cacheOnce    sync.Once
	payloadCache *dataCache
}

func (s *Store) Get(ctx context.Context, org int64, id bson.ObjectID) (Document, error) {
	cache := s.cache()
	epoch := cache.epoch()
	var stamp documentStamp
	e := s.Collection.FindOne(ctx, bson.M{"_id": id, "organization_id": org}, options.FindOne().SetProjection(bson.M{"_id": 1, "organization_id": 1, "revision": 1})).Decode(&stamp)
	if errors.Is(e, mongo.ErrNoDocuments) {
		return Document{}, missing
	}
	if e != nil {
		return Document{}, e
	}
	if stamp.Revision > 0 {
		if raw, found := cache.get(documentCacheKey(org, id, stamp.Revision)); found {
			var cached Document
			if bson.Unmarshal(raw, &cached) == nil && cached.ID == id && cached.OrganizationID == org && cached.Revision == stamp.Revision {
				return cached, nil
			}
		}
	}
	var d Document
	e = s.Collection.FindOne(ctx, bson.M{"_id": id, "organization_id": org}).Decode(&d)
	if errors.Is(e, mongo.ErrNoDocuments) {
		return d, missing
	}
	if e == nil && d.Revision > 0 && d.ID == id && d.OrganizationID == org {
		if raw, err := bson.Marshal(d); err == nil {
			cache.put(documentCacheKey(org, id, d.Revision), raw, documentTTL, epoch)
		}
	}
	return d, e
}
func (s *Store) Insert(ctx context.Context, d Document) (Document, bool, error) {
	_, e := s.Collection.InsertOne(ctx, d)
	if mongo.IsDuplicateKeyError(e) {
		old, e := s.Get(ctx, d.OrganizationID, d.ID)
		if e != nil {
			return old, false, e
		}
		if old.OperationHash != d.OperationHash {
			return old, false, conflict("idempotency_conflict")
		}
		return old, true, nil
	}
	if e == nil {
		s.cache().invalidate(d.OrganizationID, d.ID)
	}
	return d, false, e
}
func (s *Store) Save(ctx context.Context, d Document, revision int64, actor int64) (Document, error) {
	return s.save(ctx, d, revision, actor, nil)
}
func (s *Store) EnqueueSave(ctx context.Context, d Document, revision int64, actor int64, event Event) (Document, error) {
	return s.save(ctx, d, revision, actor, &event)
}
func (s *Store) save(ctx context.Context, d Document, revision int64, actor int64, event *Event) (Document, error) {
	d.Revision = revision + 1
	d.UpdatedBy = actor
	d.UpdatedAt = time.Now().UTC()
	fields, e := documentFields(d)
	if e != nil {
		return d, e
	}
	update := bson.M{"$set": fields}
	if event != nil {
		pending, err := storeEvent(*event)
		if err != nil {
			return d, err
		}
		enqueueOutbox(update, pending)
	}
	result, e := s.Collection.UpdateOne(ctx, bson.M{"_id": d.ID, "organization_id": d.OrganizationID, "revision": revision}, update)
	if e != nil {
		return d, e
	}
	if result.MatchedCount != 1 {
		s.cache().invalidate(d.OrganizationID, d.ID)
		return d, conflict("stale_revision")
	}
	s.cache().invalidate(d.OrganizationID, d.ID)
	return d, nil
}
func (s *Store) List(ctx context.Context, filter bson.M, skip, limit int64) ([]Document, error) {
	org, scoped := filter["organization_id"].(int64)
	if !scoped || org < 1 {
		return s.listUncached(ctx, filter, skip, limit)
	}
	cache := s.cache()
	epoch := cache.epoch()
	key := catalogCacheKey(org, filter, skip, limit)
	cur, e := s.Collection.Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}, {Key: "_id", Value: -1}}).SetSkip(skip).SetLimit(limit).SetProjection(bson.M{"_id": 1, "organization_id": 1, "revision": 1}))
	if e != nil {
		return nil, e
	}
	stamps := []documentStamp{}
	e = cur.All(ctx, &stamps)
	cur.Close(ctx)
	if e != nil {
		return nil, e
	}
	if !cacheableStamps(org, stamps) {
		return s.listUncached(ctx, filter, skip, limit)
	}
	if raw, found := cache.get(key); found {
		var cached catalogPayload
		if bson.Unmarshal(raw, &cached) == nil && reflect.DeepEqual(stamps, cached.Stamps) {
			return cached.Items, nil
		}
	}
	items, e := s.listUncached(ctx, filter, skip, limit)
	if e != nil {
		return nil, e
	}
	if reflect.DeepEqual(stamps, stampsForDocuments(items)) {
		if raw, err := bson.Marshal(catalogPayload{stamps, items}); err == nil {
			cache.put(key, raw, catalogTTL, epoch)
		}
	}
	return items, nil
}
func (s *Store) listUncached(ctx context.Context, filter bson.M, skip, limit int64) ([]Document, error) {
	cursor, e := s.Collection.Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}, {Key: "_id", Value: -1}}).SetSkip(skip).SetLimit(limit))
	if e != nil {
		return nil, e
	}
	defer cursor.Close(ctx)
	out := []Document{}
	e = cursor.All(ctx, &out)
	return out, e
}
