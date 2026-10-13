package calculate

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

var ErrLeaseLost = errors.New("inbox processing lease lost")

type ProcessingRepository interface {
	Claim(context.Context, time.Time, string) (Message, error)
	Checkpoint(context.Context, Message, string, json.RawMessage) error
	Finish(context.Context, Message, string, string, json.RawMessage) error
	Retry(context.Context, Message, string, time.Time) error
}

const processLease = 60 * time.Second

const (
	InitialGradeKind = "initial_grade"
	RegradeKind      = "regrade"
	ContentKind      = "content"
)

// KindProcessingRepository adds independent lanes without changing the rating
// repository contract or the processing-only mocks used by callback tests.
type KindProcessingRepository interface {
	ProcessingRepository
	ClaimKind(context.Context, time.Time, string, string) (Message, error)
}

func kindIndex(kind string) int {
	switch kind {
	case InitialGradeKind:
		return 0
	case RegradeKind:
		return 1
	case ContentKind:
		return 2
	default:
		return -1
	}
}

func processingKind(m Message) string {
	if m.Type == "tests.content.changed" {
		return ContentKind
	}
	if m.Type != "tests.grade.requested" {
		return ""
	}
	var envelope Envelope
	var payload struct {
		Operation string `json:"operation"`
	}
	if json.Unmarshal(m.Body, &envelope) == nil && json.Unmarshal(envelope.Payload, &payload) == nil && payload.Operation == "regrade" {
		return RegradeKind
	}
	// Older messages omitted operation. Unsupported or corrupt inputs are also
	// sent through a worker so they become failed rather than remain invisible.
	return InitialGradeKind
}

func scheduleTime(m Message) time.Time {
	if m.Status == "processing" && m.LeaseUntil != nil {
		return m.LeaseUntil.UTC()
	}
	if m.Status == "retrying" && m.NextAttemptAt != nil {
		return m.NextAttemptAt.UTC()
	}
	if !m.ReceivedAt.IsZero() {
		return m.ReceivedAt.UTC()
	}
	return time.Now().UTC()
}

// ClaimKind rotates received/retrying/expired-processing buckets so neither
// recovery nor new submissions starves. Each atomic claim has equality on the
// first two index keys; the due-time ordering is supplied by inbox_schedule.
// No global OR query or blocking sort across all pending messages is needed.
func (s *Store) ClaimKind(ctx context.Context, now time.Time, token, kind string) (Message, error) {
	i := kindIndex(kind)
	if i < 0 {
		return Message{}, errors.New("invalid processing kind")
	}
	statuses := [...]string{"received", "retrying", "processing"}
	start := (s.claimRotation[i].Add(1) - 1) % uint64(len(statuses))
	for offset := range statuses {
		status := statuses[(int(start)+offset)%len(statuses)]
		query := bson.M{"processing_kind": kind, "status": status, "available_at": bson.M{"$lte": now}}
		// Check the original fields too: a still-running older service version
		// does not maintain available_at, but its lease must remain authoritative.
		if status == "processing" {
			query["lease_until"] = bson.M{"$lte": now}
		}
		if status == "retrying" {
			query["next_attempt_at"] = bson.M{"$lte": now}
		}
		var m Message
		update := bson.M{"$set": bson.M{"status": "processing", "lease_token": token, "lease_until": now.Add(processLease), "available_at": now.Add(processLease)}, "$inc": bson.M{"processing_attempts": 1}}
		err := s.Collection.FindOneAndUpdate(ctx, query, update, options.FindOneAndUpdate().SetHint("inbox_schedule").SetSort(bson.D{{Key: "available_at", Value: 1}, {Key: "_id", Value: 1}}).SetReturnDocument(options.After)).Decode(&m)
		if err == nil || !noJob(err) {
			return m, err
		}
	}
	return Message{}, mongo.ErrNoDocuments
}

// Backfill only active legacy receipts. Finished audit history is left intact.
// Status and lease/next-attempt values fence updates against an old process
// that may still be completing work during a rolling deployment.
func (s *Store) migrateProcessingMetadata(ctx context.Context) error {
	query := bson.M{"status": bson.M{"$in": bson.A{"received", "retrying", "processing"}}, "$or": bson.A{bson.M{"processing_kind": bson.M{"$exists": false}}, bson.M{"available_at": bson.M{"$exists": false}}}}
	cur, err := s.Collection.Find(ctx, query, options.Find().SetBatchSize(100))
	if err != nil {
		return err
	}
	defer cur.Close(ctx)
	for cur.Next(ctx) {
		var m Message
		if err := cur.Decode(&m); err != nil {
			return err
		}
		kind := processingKind(m)
		if kind == "" {
			continue
		}
		filter := bson.M{"_id": m.ID, "status": m.Status, "lease_token": m.LeaseToken, "lease_until": m.LeaseUntil, "next_attempt_at": m.NextAttemptAt}
		if m.LeaseToken == "" {
			filter["lease_token"] = bson.M{"$in": bson.A{"", nil}}
		}
		if _, err := s.Collection.UpdateOne(ctx, filter, bson.M{"$set": bson.M{"processing_kind": kind, "available_at": scheduleTime(m)}}); err != nil {
			return err
		}
	}
	return cur.Err()
}

func (s *Store) Claim(ctx context.Context, now time.Time, token string) (Message, error) {
	var m Message
	query := bson.M{"$or": bson.A{
		bson.M{"status": "received"},
		bson.M{"status": "retrying", "next_attempt_at": bson.M{"$lte": now}},
		bson.M{"status": "processing", "lease_until": bson.M{"$lte": now}},
	}}
	update := bson.M{"$set": bson.M{"status": "processing", "lease_token": token, "lease_until": now.Add(processLease)}, "$inc": bson.M{"processing_attempts": 1}}
	err := s.Collection.FindOneAndUpdate(ctx, query, update, options.FindOneAndUpdate().SetSort(bson.D{{Key: "received_at", Value: 1}, {Key: "_id", Value: 1}}).SetReturnDocument(options.After)).Decode(&m)
	return m, err
}

func (s *Store) claimedUpdate(ctx context.Context, m Message, update bson.M) error {
	result, err := s.Collection.UpdateOne(ctx, bson.M{"_id": m.ID, "status": "processing", "lease_token": m.LeaseToken}, update)
	if err != nil {
		return err
	}
	if result.MatchedCount != 1 {
		return ErrLeaseLost
	}
	return nil
}

func (s *Store) Checkpoint(ctx context.Context, m Message, cursor string, output json.RawMessage) error {
	until := time.Now().UTC().Add(processLease)
	return s.claimedUpdate(ctx, m, bson.M{"$set": bson.M{"process_cursor": cursor, "output": []byte(output), "lease_until": until, "available_at": until}})
}

func (s *Store) Finish(ctx context.Context, m Message, status, reason string, output json.RawMessage) error {
	return s.claimedUpdate(ctx, m, bson.M{"$set": bson.M{"status": status, "last_error": reason, "processed_at": time.Now().UTC(), "output": []byte(output)}, "$unset": bson.M{"lease_token": "", "lease_until": "", "next_attempt_at": "", "available_at": ""}})
}

func (s *Store) Retry(ctx context.Context, m Message, reason string, next time.Time) error {
	return s.claimedUpdate(ctx, m, bson.M{"$set": bson.M{"status": "retrying", "last_error": reason, "next_attempt_at": next, "available_at": next}, "$unset": bson.M{"lease_token": "", "lease_until": ""}})
}

func (s *Store) Requeue(ctx context.Context, id string) (bool, error) {
	m, err := s.Get(ctx, id)
	if err != nil {
		return false, err
	}
	fields := bson.M{"status": "received", "available_at": time.Now().UTC()}
	if kind := processingKind(m); kind != "" {
		fields["processing_kind"] = kind
	}
	result, err := s.Collection.UpdateOne(ctx, bson.M{"_id": id, "status": bson.M{"$in": bson.A{"failed", "retrying"}}}, bson.M{"$set": fields, "$unset": bson.M{"lease_token": "", "lease_until": "", "next_attempt_at": "", "processed_at": "", "last_error": ""}})
	if err != nil {
		return false, err
	}
	return result.MatchedCount == 1, nil
}

func noJob(err error) bool { return errors.Is(err, mongo.ErrNoDocuments) }
