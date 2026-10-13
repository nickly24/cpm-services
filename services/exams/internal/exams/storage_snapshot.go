package exams

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

type immutableDefinition struct {
	Config    Config     `json:"config"`
	Parts     []Part     `json:"parts"`
	Questions []Question `json:"questions"`
}

// This envelope is only a persistence format. HTTP responses are always built
// by role-specific query DTOs and never serialize the aggregate.
type examDocument struct {
	Exam
	FormatVersion int                            `json:"document_version,omitempty"`
	Definitions   map[string]immutableDefinition `json:"immutable_definitions,omitempty"`
}
type definitionIdentity struct {
	part             *Part
	question         *Question
	parts, questions int
	config           string
}

// encodeExam shares content-addressed definitions across attempts. It does not
// mutate the aggregate. Slice-identity caching is safe because started attempt
// definitions are immutable and hydrated definitions share their backing data.
func encodeExam(e *Exam) ([]byte, error) {
	document := examDocument{Exam: *e, FormatVersion: 1, Definitions: map[string]immutableDefinition{}}
	document.Divisions = make(map[int64]*Division, len(e.Divisions))
	cache := map[definitionIdentity]string{}
	for id, d := range e.Divisions {
		scope := *d
		scope.Attempts = append([]Attempt(nil), d.Attempts...)
		for i := range scope.Attempts {
			at := &scope.Attempts[i]
			if len(at.Parts) == 0 && len(at.Questions) == 0 {
				if at.DefinitionKey != "" {
					return nil, errors.New("missing immutable exam definition")
				}
				continue
			}
			config, err := json.Marshal(at.Config)
			if err != nil {
				return nil, err
			}
			identity := definitionIdentity{parts: len(at.Parts), questions: len(at.Questions), config: string(config)}
			if len(at.Parts) > 0 {
				identity.part = &at.Parts[0]
			}
			if len(at.Questions) > 0 {
				identity.question = &at.Questions[0]
			}
			key, ok := cache[identity]
			if !ok {
				definition := immutableDefinition{at.Config, at.Parts, at.Questions}
				raw, err := json.Marshal(definition)
				if err != nil {
					return nil, err
				}
				digest := sha256.Sum256(raw)
				key = hex.EncodeToString(digest[:])
				cache[identity] = key
				document.Definitions[key] = definition
			}
			at.DefinitionKey = key
			at.Config = Config{}
			at.Parts = nil
			at.Questions = nil
		}
		document.Divisions[id] = &scope
	}
	return json.Marshal(document)
}

// decodeExam accepts both the original nested aggregate and the compact
// version. A missing or altered content-addressed definition fails closed.
// Shared slices must only be read; domain commands preserve this immutability.
func decodeExam(raw []byte, nextID int64) (*Exam, error) {
	var document examDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, err
	}
	if document.FormatVersion < 0 || document.FormatVersion > 1 {
		return nil, errors.New("unsupported exam document version")
	}
	// Initialize once before operations alter the legacy operational revisions.
	// Persisted semantic revisions then stay stable for irrelevant mutations.
	document.Exam.RatingRevision = examRatingRevision(&document.Exam)
	for key, definition := range document.Definitions {
		raw, err := json.Marshal(definition)
		if err != nil {
			return nil, err
		}
		digest := sha256.Sum256(raw)
		if hex.EncodeToString(digest[:]) != key {
			return nil, errors.New("invalid immutable exam definition checksum")
		}
	}
	for _, d := range document.Divisions {
		if d == nil {
			return nil, errors.New("invalid exam division")
		}
		d.RatingRevision = divisionRatingRevision(d)
		for i := range d.Attempts {
			at := &d.Attempts[i]
			if at.DefinitionKey == "" {
				continue
			}
			definition, ok := document.Definitions[at.DefinitionKey]
			if !ok {
				return nil, errors.New("missing immutable exam definition")
			}
			if len(at.Parts) > 0 || len(at.Questions) > 0 {
				return nil, errors.New("ambiguous immutable exam definition")
			}
			at.Config = definition.Config
			at.Parts = definition.Parts
			at.Questions = definition.Questions
		}
	}
	document.Exam.NextID = nextID
	return &document.Exam, nil
}
