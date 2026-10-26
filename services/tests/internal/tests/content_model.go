package tests

import (
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

const ContentCollectionName = "tests_content_versions_v1"

type AnswerOption struct {
	ID        string `json:"id" bson:"id"`
	Text      string `json:"text" bson:"text"`
	IsCorrect bool   `json:"is_correct" bson:"is_correct"`
}
type Question struct {
	QuestionID     int64          `json:"question_id" bson:"question_id"`
	Type           string         `json:"type" bson:"type"`
	Text           string         `json:"text" bson:"text"`
	Points         int            `json:"points" bson:"points"`
	Answers        []AnswerOption `json:"answers" bson:"answers"`
	CorrectAnswers []string       `json:"correct_answers" bson:"correct_answers"`
}
type StudentOption struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}
type StudentQuestion struct {
	QuestionID int64           `json:"question_id"`
	Type       string          `json:"type"`
	Text       string          `json:"text"`
	Points     int             `json:"points"`
	Answers    []StudentOption `json:"answers"`
}

func SafeQuestions(qs []Question) []StudentQuestion {
	out := make([]StudentQuestion, 0, len(qs))
	for _, q := range qs {
		v := StudentQuestion{QuestionID: q.QuestionID, Type: q.Type, Text: q.Text, Points: q.Points, Answers: []StudentOption{}}
		for _, a := range q.Answers {
			v.Answers = append(v.Answers, StudentOption{a.ID, a.Text})
		}
		out = append(out, v)
	}
	return out
}

type ContentSnapshot struct {
	Title       string     `json:"title" bson:"title"`
	Description string     `json:"description" bson:"description"`
	DirectionID int64      `json:"direction_id" bson:"direction_id"`
	Questions   []Question `json:"questions" bson:"questions"`
}
type ContentChange struct {
	Kind       string `json:"kind" bson:"kind"`
	QuestionID *int64 `json:"question_id,omitempty" bson:"question_id,omitempty"`
	Before     any    `json:"before" bson:"before"`
	After      any    `json:"after" bson:"after"`
}
type ChangeBatch struct {
	EventID                      string          `json:"event_id,omitempty" bson:"event_id,omitempty"`
	RecalculateOnChange          *bool           `json:"recalculate_on_test_change,omitempty" bson:"recalculate_on_test_change,omitempty"`
	OrganizationSettingsRevision int64           `json:"organization_settings_revision" bson:"organization_settings_revision"`
	BatchID                      bson.ObjectID   `json:"batch_id" bson:"batch_id"`
	TestID                       bson.ObjectID   `json:"test_id" bson:"test_id"`
	OrganizationID               int64           `json:"organization_id" bson:"organization_id"`
	OwnerDivisionID              int64           `json:"owner_division_id" bson:"owner_division_id"`
	ActorID                      int64           `json:"actor_id" bson:"actor_id"`
	ActorRole                    string          `json:"actor_role" bson:"actor_role"`
	ChangedAt                    time.Time       `json:"changed_at" bson:"changed_at"`
	BeforeVersionID              bson.ObjectID   `json:"before_version_id" bson:"before_version_id"`
	AfterVersionID               bson.ObjectID   `json:"after_version_id" bson:"after_version_id"`
	Before                       ContentSnapshot `json:"before" bson:"before"`
	After                        ContentSnapshot `json:"after" bson:"after"`
	Changes                      []ContentChange `json:"changes" bson:"changes"`
}
type ContentVersion struct {
	ID             bson.ObjectID `json:"id" bson:"_id"`
	TestID         bson.ObjectID `json:"test_id" bson:"test_id"`
	OrganizationID int64         `json:"organization_id" bson:"organization_id"`
	Title          string        `json:"title" bson:"title"`
	Description    string        `json:"description" bson:"description"`
	DirectionID    int64         `json:"direction_id" bson:"direction_id"`
	Questions      []Question    `json:"questions" bson:"questions"`
	CreatedAt      time.Time     `json:"created_at" bson:"created_at"`
	ActorID        int64         `json:"actor_id" bson:"actor_id"`
	Batch          *ChangeBatch  `json:"change_batch" bson:"change_batch"`
}
type ContentDTO struct {
	ExpectedRevision int64 `json:"expected_revision"`
	ContentSnapshot
}
type ContentView struct {
	TestID        bson.ObjectID       `json:"test_id"`
	Revision      int64               `json:"revision"`
	VersionID     bson.ObjectID       `json:"content_version_id"`
	MaxQuestionID int64               `json:"max_question_id"`
	UsedOptionIDs map[string][]string `json:"used_option_ids"`
	ContentSnapshot
}
type HistoryView struct {
	Items  []ContentVersion `json:"items"`
	Total  int64            `json:"total"`
	Limit  int64            `json:"limit"`
	Offset int64            `json:"offset"`
}

func snapshot(d Document) ContentSnapshot {
	qs := d.Questions
	if qs == nil {
		qs = []Question{}
	}
	return ContentSnapshot{d.Title, d.Description, d.DirectionID, qs}
}
func contentView(d Document) ContentView {
	return ContentView{TestID: d.ID, Revision: d.Revision, VersionID: d.ContentVersionID, MaxQuestionID: d.MaxQuestionID, UsedOptionIDs: d.UsedOptionIDs, ContentSnapshot: snapshot(d)}
}

var optionIdentifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// Question and option counts are not a business limit. Validate their meaning.
func validateQuestions(qs []Question, allowEmpty bool) error {
	if !allowEmpty && len(qs) == 0 {
		return bad("invalid_question_count")
	}
	ids := map[int64]bool{}
	for _, q := range qs {
		if q.QuestionID < 1 || q.QuestionID > 9007199254740991 || ids[q.QuestionID] || !validText(q.Text, 4000, true) || q.Points < 1 || q.Points > 100000 {
			return bad("invalid_question")
		}
		ids[q.QuestionID] = true
		switch q.Type {
		case "single", "multiple":
			if len(q.Answers) < 2 || len(q.CorrectAnswers) > 0 {
				return bad("invalid_choice_question")
			}
			seen := map[string]bool{}
			correct := 0
			for _, a := range q.Answers {
				if !optionIdentifier.MatchString(a.ID) || seen[a.ID] || !validText(a.Text, 2000, true) {
					return bad("invalid_answer_option")
				}
				seen[a.ID] = true
				if a.IsCorrect {
					correct++
				}
			}
			if correct < 1 || q.Type == "single" && correct != 1 {
				return bad("invalid_correct_options")
			}
		case "text":
			if len(q.Answers) > 0 || len(q.CorrectAnswers) < 1 {
				return bad("invalid_text_question")
			}
			seen := map[string]bool{}
			for _, a := range q.CorrectAnswers {
				key := strings.ToLower(strings.TrimSpace(a))
				if !validText(a, 1000, true) || seen[key] {
					return bad("invalid_text_alias")
				}
				seen[key] = true
			}
		default:
			return bad("invalid_question_type")
		}
	}
	return nil
}
func normalizeQuestions(qs []Question) []Question {
	out := make([]Question, 0, len(qs))
	for _, q := range qs {
		q.Text = strings.TrimSpace(q.Text)
		q.Answers = append([]AnswerOption{}, q.Answers...)
		q.CorrectAnswers = append([]string{}, q.CorrectAnswers...)
		for i := range q.Answers {
			q.Answers[i].Text = strings.TrimSpace(q.Answers[i].Text)
		}
		for i := range q.CorrectAnswers {
			q.CorrectAnswers[i] = strings.TrimSpace(q.CorrectAnswers[i])
		}
		out = append(out, q)
	}
	return out
}
func transitionIDs(old Document, qs []Question) (int64, map[string][]string, error) {
	maxID := old.MaxQuestionID
	current := map[int64]Question{}
	used := map[string][]string{}
	for k, v := range old.UsedOptionIDs {
		used[k] = append([]string{}, v...)
	}
	for _, q := range old.Questions {
		current[q.QuestionID] = q
		if q.QuestionID > maxID {
			maxID = q.QuestionID
		}
	}
	previousMax := maxID
	for _, q := range qs {
		prev, exists := current[q.QuestionID]
		if !exists && q.QuestionID <= previousMax {
			return 0, nil, conflict("question_id_reused")
		}
		if q.QuestionID > maxID {
			maxID = q.QuestionID
		}
		key := strconv.FormatInt(q.QuestionID, 10)
		all := map[string]bool{}
		active := map[string]bool{}
		for _, id := range used[key] {
			all[id] = true
		}
		for _, a := range prev.Answers {
			active[a.ID] = true
			all[a.ID] = true
		}
		for _, a := range q.Answers {
			if all[a.ID] && !active[a.ID] {
				return 0, nil, conflict("answer_id_reused")
			}
			all[a.ID] = true
		}
		list := []string{}
		for id := range all {
			list = append(list, id)
		}
		sort.Strings(list)
		used[key] = list
	}
	return maxID, used, nil
}
func contentChanges(before, after ContentSnapshot) []ContentChange {
	out := []ContentChange{}
	if before.Title != after.Title || before.Description != after.Description || before.DirectionID != after.DirectionID {
		b, a := before, after
		b.Questions = nil
		a.Questions = nil
		out = append(out, ContentChange{Kind: "metadata_updated", Before: b, After: a})
	}
	bmap, amap := map[int64]Question{}, map[int64]Question{}
	bo, ao := []int64{}, []int64{}
	for _, q := range before.Questions {
		bmap[q.QuestionID] = q
		bo = append(bo, q.QuestionID)
	}
	for _, q := range after.Questions {
		amap[q.QuestionID] = q
		ao = append(ao, q.QuestionID)
	}
	for _, q := range before.Questions {
		id := q.QuestionID
		if nq, ok := amap[id]; !ok {
			out = append(out, ContentChange{"question_removed", &id, q, nil})
		} else if !reflect.DeepEqual(q, nq) {
			out = append(out, ContentChange{"question_updated", &id, q, nq})
		}
	}
	for _, q := range after.Questions {
		if _, ok := bmap[q.QuestionID]; !ok {
			id := q.QuestionID
			out = append(out, ContentChange{"question_added", &id, nil, q})
		}
	}
	if !reflect.DeepEqual(bo, ao) {
		out = append(out, ContentChange{Kind: "questions_reordered", Before: bo, After: ao})
	}
	return out
}
