package calculate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const (
	RatingsQueueName      = "calculate.ratings.v1"
	RatingsDeadQueue      = "calculate.ratings.dead.v1"
	RatingsDeadRoute      = "calculate.ratings.invalid.v1"
	RatingsCollectionName = "calculate_ratings_inbox_v1"
	RatingsMessageType    = "ratings.calculate.requested"
	RatingsRoutingKey     = "ratings.calculate.requested.v1"
)

type RatingRequest struct {
	JobID          string `json:"job_id"`
	Revision       int64  `json:"revision"`
	OrganizationID int64  `json:"organization_id"`
	DivisionID     int64  `json:"division_id"`
	AccountID      int64  `json:"account_id"`
	InputHash      string `json:"input_hash"`
}

func validRatingRequest(r RatingRequest) bool {
	if !uuidPattern.MatchString(r.JobID) || r.Revision < 1 || r.OrganizationID < 1 || r.DivisionID < 1 || r.AccountID < 1 || len(r.InputHash) != 64 {
		return false
	}
	_, err := hex.DecodeString(r.InputHash)
	return err == nil
}

func ParseRatingMessage(body []byte, route string, now time.Time) (Message, error) {
	invalid := func(reason string) (Message, error) {
		return Message{}, fmt.Errorf("%w: %s", ErrInvalidMessage, reason)
	}
	if len(body) == 0 || len(body) > MaxMessageBytes || validateJSON(body) != nil {
		return invalid("JSON or size")
	}
	var e Envelope
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if d.Decode(&e) != nil || !uuidPattern.MatchString(e.MessageID) || e.SchemaVersion != 1 || e.Producer != "ratings-service" || e.MessageType != RatingsMessageType || route != RatingsRoutingKey || e.OccurredAt.IsZero() || len(e.CorrelationID) == 0 || len(e.CorrelationID) > 128 || len(e.CausationID) > 128 {
		return invalid("envelope")
	}
	_, offset := e.OccurredAt.Zone()
	if offset != 0 {
		return invalid("occurred_at must be UTC")
	}
	var request RatingRequest
	d = json.NewDecoder(bytes.NewReader(e.Payload))
	d.DisallowUnknownFields()
	if d.Decode(&request) != nil || !validRatingRequest(request) {
		return invalid("rating identity")
	}
	canonical, err := canonicalPayload(e.Payload)
	if err != nil {
		return invalid("payload")
	}
	bodyHash, payloadHash := sha256.Sum256(body), sha256.Sum256(canonical)
	return Message{ID: e.MessageID, Type: e.MessageType, SchemaVersion: e.SchemaVersion, OccurredAt: e.OccurredAt.UTC(), Producer: e.Producer, CorrelationID: e.CorrelationID, CausationID: e.CausationID, OrganizationID: request.OrganizationID, DivisionID: request.DivisionID, AccountID: request.AccountID, JobID: request.JobID, Revision: request.Revision, InputHash: request.InputHash, BodyHash: hex.EncodeToString(bodyHash[:]), PayloadHash: hex.EncodeToString(payloadHash[:]), Body: append([]byte(nil), body...), ReceivedAt: now.UTC(), Status: "received"}, nil
}

func DeclareRatingsTopology(ch *amqp.Channel) error {
	for _, exchange := range []string{ExchangeName, DeadExchange} {
		if err := ch.ExchangeDeclare(exchange, "topic", true, false, false, false, nil); err != nil {
			return err
		}
	}
	if _, err := ch.QueueDeclare(RatingsDeadQueue, true, false, false, false, amqp.Table{"x-queue-type": "quorum", "x-delivery-limit": int32(-1)}); err != nil {
		return err
	}
	if err := ch.QueueBind(RatingsDeadQueue, RatingsDeadRoute, DeadExchange, false, nil); err != nil {
		return err
	}
	if _, err := ch.QueueDeclare(RatingsQueueName, true, false, false, false, amqp.Table{
		"x-queue-type": "quorum", "x-delivery-limit": int32(-1),
		"x-dead-letter-exchange": DeadExchange, "x-dead-letter-routing-key": RatingsDeadRoute,
		"x-dead-letter-strategy": "at-least-once", "x-overflow": "reject-publish",
	}); err != nil {
		return err
	}
	return ch.QueueBind(RatingsQueueName, RatingsRoutingKey, ExchangeName, false, nil)
}

func (s *Store) EnsureRatingsIndexes(ctx context.Context) error {
	_, err := s.Collection.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "job_id", Value: 1}, {Key: "account_id", Value: 1}, {Key: "received_at", Value: -1}}, Options: options.Index().SetName("rating_job_messages")},
		{Keys: bson.D{{Key: "status", Value: 1}, {Key: "next_attempt_at", Value: 1}, {Key: "lease_until", Value: 1}, {Key: "received_at", Value: 1}}, Options: options.Index().SetName("inbox_processing")},
	})
	return err
}
