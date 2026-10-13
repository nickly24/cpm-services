package tests

import (
	"encoding/json"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

const DraftCollectionName = "tests_drafts_v1"

type DraftPosition struct {
	QuestionID int64   `json:"question_id" bson:"question_id"`
	X          float64 `json:"x" bson:"x"`
	Y          float64 `json:"y" bson:"y"`
}
type DraftLock struct {
	ActorID   int64     `json:"actor_id" bson:"actor_id"`
	ExpiresAt time.Time `json:"expires_at" bson:"expires_at"`
}
type DraftDTO struct {
	Title       string          `json:"title"`
	Description string          `json:"description"`
	DirectionID int64           `json:"direction_id"`
	Questions   []Question      `json:"questions"`
	Layout      []DraftPosition `json:"layout"`
}
type DraftEditDTO struct {
	ExpectedRevision int64 `json:"expected_revision"`
	DraftDTO
}
type Draft struct {
	ID                        bson.ObjectID   `json:"id" bson:"_id"`
	OrganizationID            int64           `json:"organization_id" bson:"organization_id"`
	DivisionID                int64           `json:"division_id" bson:"division_id"`
	Revision                  int64           `json:"revision" bson:"revision"`
	Title                     string          `json:"title" bson:"title"`
	Description               string          `json:"description" bson:"description"`
	DirectionID               int64           `json:"direction_id" bson:"direction_id"`
	Questions                 []Question      `json:"questions" bson:"questions"`
	Layout                    []DraftPosition `json:"layout" bson:"layout"`
	Lock                      *DraftLock      `json:"lock" bson:"lock"`
	State                     string          `json:"state" bson:"state"`
	SourceTestID              *bson.ObjectID  `json:"source_test_id" bson:"source_test_id"`
	PublishedTestID           *bson.ObjectID  `json:"published_test_id" bson:"published_test_id"`
	PublicationSourceRevision int64           `json:"publication_source_revision,omitempty" bson:"publication_source_revision"`
	PublisherID               int64           `json:"publisher_id,omitempty" bson:"publisher_id"`
	PublicationStartedAt      *time.Time      `json:"publication_started_at,omitempty" bson:"publication_started_at"`
	CreatedBy                 int64           `json:"created_by" bson:"created_by"`
	UpdatedBy                 int64           `json:"updated_by" bson:"updated_by"`
	CreatedAt                 time.Time       `json:"created_at" bson:"created_at"`
	UpdatedAt                 time.Time       `json:"updated_at" bson:"updated_at"`
	OperationHash             string          `json:"-" bson:"operation_hash"`
	// Only restored legacy drafts carry their pre-migration publication settings.
	LegacyPublicationSettings *Settings `json:"legacy_publication_settings,omitempty" bson:"legacy_publication_settings,omitempty"`
}
type DraftListView struct {
	Items  []Draft `json:"items"`
	Total  int64   `json:"total"`
	Limit  int64   `json:"limit"`
	Offset int64   `json:"offset"`
}

func validateDraft(in DraftDTO) error {
	if !validText(in.Title, 200, false) || !validText(in.Description, 2000, false) || in.DirectionID < 0 || len(in.Questions) > 200 || len(in.Layout) > 200 {
		return bad("invalid_draft")
	}
	ids := map[int64]bool{}
	for _, q := range in.Questions {
		if q.QuestionID < 1 || q.QuestionID > 9007199254740991 || ids[q.QuestionID] || q.Points < 0 || q.Points > 100000 || !validText(q.Text, 4000, false) {
			return bad("invalid_draft_question")
		}
		ids[q.QuestionID] = true
		if q.Type != "" && q.Type != "single" && q.Type != "multiple" && q.Type != "text" || len(q.Answers) > 50 || len(q.CorrectAnswers) > 50 {
			return bad("invalid_draft_question")
		}
		options := map[string]bool{}
		for _, a := range q.Answers {
			if !optionIdentifier.MatchString(a.ID) || options[a.ID] || !validText(a.Text, 2000, false) {
				return bad("invalid_draft_answer")
			}
			options[a.ID] = true
		}
		for _, a := range q.CorrectAnswers {
			if !validText(a, 1000, false) {
				return bad("invalid_draft_alias")
			}
		}
	}
	positions := map[int64]bool{}
	for _, p := range in.Layout {
		if !ids[p.QuestionID] || positions[p.QuestionID] || p.X < -1000000 || p.X > 1000000 || p.Y < -1000000 || p.Y > 1000000 {
			return bad("invalid_draft_layout")
		}
		positions[p.QuestionID] = true
	}
	raw, e := json.Marshal(in)
	if e != nil || len(raw) > 400000 {
		return bad("draft_too_large")
	}
	return nil
}
