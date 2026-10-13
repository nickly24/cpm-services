package tests

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const (
	outboxReadyIndex     = "outbox_ready_v1"
	outboxBatchSize      = 32
	outboxSliceSize      = 8
	outboxLease          = 60 * time.Second
	outboxDBTimeout      = 10 * time.Second
	outboxConfirmTimeout = 10 * time.Second
)

var errOutboxLeaseLost = errors.New("outbox lease lost")

type leasedOutbox struct {
	ID         bson.ObjectID `bson:"_id"`
	Outbox     []storedEvent `bson:"outbox"`
	LeaseToken string        `bson:"outbox_lease_token"`
	collection *mongo.Collection
}

// Enqueue remains part of the same CAS update as the accepted answers/content.
// In particular it must not reset a publisher's current lease.
func enqueueOutbox(update bson.M, pending storedEvent) {
	update["$set"].(bson.M)["outbox_pending"] = true
	update["$push"] = bson.M{"outbox": pending}
}

func ensureCollectionOutbox(ctx context.Context, collection *mongo.Collection) error {
	_, err := collection.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "outbox_lease_until", Value: 1}, {Key: "_id", Value: 1}},
		Options: options.Index().SetName(outboxReadyIndex).SetPartialFilterExpression(bson.M{"outbox_pending": true}),
	})
	if err != nil {
		return err
	}
	// One idempotent migration scan at startup, rather than a full collection scan
	// on every idle poll. Do not change leases or already marked documents.
	_, err = collection.UpdateMany(ctx,
		bson.M{"outbox_pending": bson.M{"$exists": false}, "outbox.0": bson.M{"$exists": true}},
		bson.M{"$set": bson.M{"outbox_pending": true}})
	return err
}

func (s *Store) EnsureOutboxIndexes(ctx context.Context) error {
	for _, collection := range []*mongo.Collection{s.Collection, s.Attempts()} {
		if err := ensureCollectionOutbox(ctx, collection); err != nil {
			return err
		}
	}
	return nil
}

func claimOutbox(ctx context.Context, collection *mongo.Collection, limit int, now time.Time) (leasedOutbox, error) {
	var record leasedOutbox
	filter := bson.M{"outbox_pending": true, "$or": bson.A{
		bson.M{"outbox_lease_until": nil},
		bson.M{"outbox_lease_until": bson.M{"$lte": now}},
	}}
	update := bson.M{"$set": bson.M{"outbox_lease_until": now.Add(outboxLease), "outbox_lease_token": uuid()}}
	err := collection.FindOneAndUpdate(ctx, filter, update,
		options.FindOneAndUpdate().SetHint(outboxReadyIndex).
			SetSort(bson.D{{Key: "outbox_lease_until", Value: 1}, {Key: "_id", Value: 1}}).
			SetProjection(bson.M{"outbox": bson.M{"$slice": limit}, "outbox_lease_token": 1}).
			SetReturnDocument(options.After)).Decode(&record)
	record.collection = collection
	return record, err
}

// Removing confirmed IDs and computing the pending marker happen in one Mongo
// update. An enqueue concurrent with this operation is retained in either order.
func finishOutbox(ctx context.Context, record leasedOutbox, confirmed []string) error {
	pipeline := mongo.Pipeline{}
	if len(confirmed) > 0 {
		pipeline = append(pipeline, bson.D{{Key: "$set", Value: bson.M{"outbox": bson.M{"$filter": bson.M{
			"input": bson.M{"$ifNull": bson.A{"$outbox", bson.A{}}}, "as": "event",
			"cond": bson.M{"$not": bson.A{bson.M{"$in": bson.A{"$$event.id", bson.M{"$literal": confirmed}}}}},
		}}}}})
	}
	pipeline = append(pipeline,
		bson.D{{Key: "$set", Value: bson.M{"outbox_pending": bson.M{"$gt": bson.A{
			bson.M{"$size": bson.M{"$ifNull": bson.A{"$outbox", bson.A{}}}}, 0,
		}}}}},
		bson.D{{Key: "$unset", Value: bson.A{"outbox_lease_until", "outbox_lease_token"}}},
	)
	result, err := record.collection.UpdateOne(ctx,
		bson.M{"_id": record.ID, "outbox_lease_token": record.LeaseToken}, pipeline)
	if err != nil {
		return err
	}
	if result.MatchedCount != 1 {
		return errOutboxLeaseLost
	}
	return nil
}

func (s *Store) claimOutboxBatch(ctx context.Context) ([]leasedOutbox, error) {
	// Both collections get a bounded share; bulk content changes cannot starve
	// freshly accepted submissions. One deadline bounds all database claims.
	ctx, cancel := context.WithTimeout(ctx, outboxDBTimeout)
	defer cancel()
	batch := []leasedOutbox{}
	for _, collection := range []*mongo.Collection{s.Collection, s.Attempts()} {
		remaining := outboxBatchSize / 2
		for remaining > 0 {
			record, err := claimOutbox(ctx, collection, min(remaining, outboxSliceSize), time.Now().UTC())
			if errors.Is(err, mongo.ErrNoDocuments) {
				break
			}
			if err != nil {
				// Claims already made safely become available when their lease expires.
				return nil, err
			}
			if len(record.Outbox) == 0 {
				if err = finishOutbox(ctx, record, nil); err != nil {
					return nil, err
				}
				continue
			}
			batch = append(batch, record)
			remaining -= len(record.Outbox)
		}
	}
	return batch, nil
}
