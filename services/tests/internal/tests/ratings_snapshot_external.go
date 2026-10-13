package tests

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// External tests are division-local aggregates. A copy in another division has
// its own test ID and results; no global result is expanded across divisions.
const externalRatingsCollection = "tests_external_catalog_v1"
const externalRatingsDomainCollection = ExternalDomainCollection

// The owner writes this durable marker only after its business collections and
// indexes are initialized. Mongo returns an empty cursor for a missing catalogue
// too, so catalogue reads alone cannot prove that the external domain exists.
func externalDomainReady(ctx context.Context, store *Store) error {
	var marker struct {
		ID            string `bson:"_id"`
		SchemaVersion int    `bson:"schema_version"`
		Status        string `bson:"status"`
	}
	e := store.Collection.Database().Collection(externalRatingsDomainCollection).FindOne(ctx,
		bson.M{"_id": "external-tests"}, options.FindOne().SetProjection(bson.M{"_id": 1, "schema_version": 1, "status": 1})).Decode(&marker)
	if e != nil || marker.ID != "external-tests" || marker.SchemaVersion != 1 || marker.Status != "ready" {
		return &APIError{Code: 503, Message: "external_domain_unavailable"}
	}
	return nil
}

type externalRatingDocument struct {
	ID             bson.ObjectID          `bson:"_id"`
	OrganizationID int64                  `bson:"organization_id"`
	DivisionID     int64                  `bson:"division_id"`
	Name           string                 `bson:"name"`
	DirectionID    int64                  `bson:"direction_id"`
	Date           string                 `bson:"date"`
	Revision       int64                  `bson:"revision"`
	Deleted        bool                   `bson:"is_deleted"`
	Results        []externalRatingResult `bson:"results"`
}

type externalRatingResult struct {
	ID        bson.ObjectID `bson:"_id"`
	Revision  int64         `bson:"revision"`
	AccountID int64         `bson:"account_id"`
	Score     *float64      `bson:"score"`
}

func ratingExternalCatalogFilter(in RatingSnapshotRequest) bson.M {
	return bson.M{"organization_id": in.OrganizationID, "division_id": in.DivisionID,
		"is_deleted": bson.M{"$ne": true}, "date": bson.M{"$gte": in.DateFrom, "$lte": in.DateTo}}
}

func (s mongoRatingSource) externalCatalog(ctx context.Context, in RatingSnapshotRequest, _, _ time.Time) ([]RatingExternalTest, error) {
	if e := externalDomainReady(ctx, s.store); e != nil {
		return nil, e
	}
	collection := s.store.Collection.Database().Collection(externalRatingsCollection)
	cursor, e := collection.Find(ctx, ratingExternalCatalogFilter(in), options.Find().
		SetLimit(ratingsMaxTests+1).SetSort(bson.D{{Key: "_id", Value: 1}}).
		SetProjection(bson.M{"_id": 1, "organization_id": 1, "division_id": 1, "name": 1,
			"direction_id": 1, "date": 1, "revision": 1, "is_deleted": 1,
			"results._id": 1, "results.revision": 1, "results.account_id": 1, "results.score": 1}))
	if e != nil {
		return nil, e
	}
	defer cursor.Close(ctx)
	out := []RatingExternalTest{}
	size := 0
	for cursor.Next(ctx) {
		size += len(cursor.Current)
		if len(out) == ratingsMaxTests || size > ratingsMaxBytes {
			return nil, ratingSnapshotTooLarge()
		}
		var d externalRatingDocument
		if e = cursor.Decode(&d); e != nil {
			return nil, e
		}
		// Recheck the returned projection too: a malformed owner document must
		// not widen scope or silently become an empty source.
		if d.ID.IsZero() || d.OrganizationID != in.OrganizationID || d.DivisionID != in.DivisionID || d.Deleted {
			return nil, conflict("rating_source_incomplete")
		}
		if len(d.Results) > ratingsMaxResults {
			return nil, ratingSnapshotTooLarge()
		}
		test := RatingExternalTest{OrganizationID: d.OrganizationID, DivisionID: d.DivisionID,
			TestID: d.ID.Hex(), Title: d.Name, DirectionID: d.DirectionID, Date: d.Date, Revision: d.Revision, Results: []RatingExternalResult{}}
		for _, r := range d.Results {
			if r.ID.IsZero() {
				return nil, conflict("rating_source_incomplete")
			}
			test.Results = append(test.Results, RatingExternalResult{AccountID: r.AccountID, ResultID: r.ID.Hex(), Revision: r.Revision, Score: r.Score})
		}
		out = append(out, test)
	}
	return out, cursor.Err()
}
