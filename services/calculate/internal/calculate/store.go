package calculate

import (
	"context"
	"errors"
	"sync/atomic"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const CollectionName = "calculate_inbox_v1"

type Filter struct {
	SubmissionID string
	TestID       string
	JobID        string
	AccountID    int64
	MetadataOnly bool
	Limit        int64
	Offset       int64
}

type Repository interface {
	Receive(context.Context, Message) (bool, error)
	Get(context.Context, string) (Message, error)
	List(context.Context, Filter) ([]Message, int64, error)
	Ready(context.Context) error
}

type Store struct {
	Collection    *mongo.Collection
	claimRotation [3]atomic.Uint64
}

func (s *Store) EnsureIndexes(ctx context.Context) error {
	_, err := s.Collection.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "submission_id", Value: 1}, {Key: "received_at", Value: -1}, {Key: "_id", Value: -1}}, Options: options.Index().SetName("submission_messages")},
		{Keys: bson.D{{Key: "test_id", Value: 1}, {Key: "received_at", Value: -1}, {Key: "_id", Value: -1}}, Options: options.Index().SetName("test_messages")},
		{Keys: bson.D{{Key: "status", Value: 1}, {Key: "next_attempt_at", Value: 1}, {Key: "lease_until", Value: 1}, {Key: "received_at", Value: 1}}, Options: options.Index().SetName("inbox_processing")},
		{Keys: bson.D{{Key: "processing_kind", Value: 1}, {Key: "status", Value: 1}, {Key: "available_at", Value: 1}, {Key: "_id", Value: 1}}, Options: options.Index().SetName("inbox_schedule")},
		{Keys: bson.D{{Key: "processing_kind", Value: 1}, {Key: "status", Value: 1}, {Key: "received_at", Value: 1}, {Key: "_id", Value: 1}}, Options: options.Index().SetName("inbox_monitoring")},
	})
	if err != nil {
		return err
	}
	return s.migrateProcessingMetadata(ctx)
}

// Receive writes the immutable envelope once. ACK must follow this successful
// majority+journal write; a redelivery only compares the persisted body hash.
func (s *Store) Receive(ctx context.Context, m Message) (bool, error) {
	if kind := processingKind(m); kind != "" {
		m.ProcessingKind = kind
		at := scheduleTime(m)
		m.AvailableAt = &at
	}
	_, err := s.Collection.InsertOne(ctx, m)
	if err == nil {
		return true, nil
	}
	if !mongo.IsDuplicateKeyError(err) {
		return false, err
	}
	existing, err := s.Get(ctx, m.ID)
	if err != nil {
		return false, err
	}
	if existing.BodyHash != m.BodyHash {
		return false, ErrMessageConflict
	}
	return false, nil
}

func (s *Store) Get(ctx context.Context, id string) (Message, error) {
	var m Message
	err := s.Collection.FindOne(ctx, bson.M{"_id": id}).Decode(&m)
	return m, err
}

func (s *Store) List(ctx context.Context, f Filter) ([]Message, int64, error) {
	query := bson.M{}
	if f.SubmissionID != "" {
		query["submission_id"] = f.SubmissionID
	}
	if f.TestID != "" {
		query["test_id"] = f.TestID
	}
	if f.JobID != "" {
		query["job_id"] = f.JobID
	}
	if f.AccountID > 0 {
		query["account_id"] = f.AccountID
	}
	total, err := s.Collection.CountDocuments(ctx, query)
	if err != nil {
		return nil, 0, err
	}
	// Fetch metadata directly from Mongo: excluding the binary envelope here
	// avoids reading and copying up to 100 multi-megabyte immutable payloads.
	projection := bson.M{"body": 0}
	if f.MetadataOnly {
		projection["output"] = 0
	}
	cur, err := s.Collection.Find(ctx, query, options.Find().SetProjection(projection).SetSort(bson.D{{Key: "received_at", Value: -1}, {Key: "_id", Value: -1}}).SetLimit(f.Limit).SetSkip(f.Offset))
	if err != nil {
		return nil, 0, err
	}
	defer cur.Close(ctx)
	items := []Message{}
	if err = cur.All(ctx, &items); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func (s *Store) Ready(ctx context.Context) error {
	return s.Collection.Database().Client().Ping(ctx, nil)
}

func IsNotFound(err error) bool { return errors.Is(err, mongo.ErrNoDocuments) }
