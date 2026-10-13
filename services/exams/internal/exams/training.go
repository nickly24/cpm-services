package exams

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

type TrainingSettings struct {
	BatchSize      int    `json:"batch_size"`
	LastBatchIndex int    `json:"last_batch_index"`
	StudyMode      string `json:"study_mode"`
	LastPartID     *int64 `json:"last_part_id"`
}

func DefaultTrainingSettings() TrainingSettings {
	return TrainingSettings{BatchSize: 10, StudyMode: "unlearned"}
}

func storedTrainingSettings(raw []byte) (TrainingSettings, error) {
	settings := DefaultTrainingSettings()
	if len(raw) == 0 {
		return settings, nil
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return settings, &APIError{503, "invalid_training_settings"}
	}
	if err := DecodeStrict(raw, &settings); err != nil {
		return settings, &APIError{503, "invalid_training_settings"}
	}
	if err := trainingSettingsValid(settings); err != nil {
		return settings, &APIError{503, "invalid_training_settings"}
	}
	return settings, nil
}

func auxDecode(input json.RawMessage, target any) error { return DecodeStrict(input, target) }

func trainingScope(e *Exam, a Actor, p Params) (*Division, error) {
	d, err := auxScope(e, a, p, false)
	if err != nil {
		return nil, err
	}
	if e.Kind != "classic" {
		return nil, Bad("wrong_exam_type")
	}
	preview := p.Query["preview"] == "true" || p.Query["preview"] == "1"
	if preview {
		if !a.Can(false) {
			return nil, Denied
		}
	} else {
		if a.Role != "student" {
			return nil, Denied
		}
		if !d.TrainingEnabled {
			return nil, Conflict("training_disabled")
		}
	}
	if p.PartID != 0 {
		found := false
		for _, part := range e.Parts {
			if part.ID == p.PartID {
				found = true
			}
		}
		if !found {
			return nil, Missing
		}
	}
	return d, nil
}
func pythonJSONString(value string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(value)
	s := strings.TrimSuffix(b.String(), "\n")
	s = strings.ReplaceAll(s, "\\u2028", "\u2028")
	return strings.ReplaceAll(s, "\\u2029", "\u2029")
}

// This byte layout matches legacy json.dumps(sort_keys=True,ensure_ascii=False).
func TrainingFingerprint(question, answer string) string {
	payload := "{\"a\": " + pythonJSONString(strings.TrimSpace(answer)) + ", \"q\": " + pythonJSONString(strings.TrimSpace(question)) + "}"
	hash := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(hash[:])
}
func trainingCardRef(e *Exam, q Question) string { return fmt.Sprintf("exam:%d:%d", e.ID, q.ID) }

type TrainingCard struct {
	CardRef     string `json:"card_ref"`
	QuestionID  int64  `json:"question_id"`
	ExamID      int64  `json:"exam_id"`
	PartID      int64  `json:"part_id"`
	PartCode    string `json:"part_code"`
	Question    string `json:"question"`
	Answer      string `json:"answer"`
	Fingerprint string `json:"content_fingerprint"`
	Status      string `json:"status"`
}
type TrainingStats struct {
	Total         int `json:"total"`
	Learned       int `json:"learned"`
	AnswerChanged int `json:"answer_changed"`
	Unlearned     int `json:"unlearned"`
	Percent       int `json:"progress_percent"`
}
type TrainingBatch struct {
	Index int           `json:"index"`
	From  int           `json:"from"`
	To    int           `json:"to"`
	Size  int           `json:"size"`
	Stats TrainingStats `json:"stats"`
}

func trainingStats(cards []TrainingCard) TrainingStats {
	s := TrainingStats{Total: len(cards)}
	for _, card := range cards {
		switch card.Status {
		case "learned":
			s.Learned++
		case "answer_changed":
			s.AnswerChanged++
		default:
			s.Unlearned++
		}
	}
	if s.Total > 0 {
		s.Percent = int(math.RoundToEven(float64(s.Learned) * 100 / float64(s.Total)))
	}
	return s
}
func trainingSettingsValid(s TrainingSettings) error {
	if s.BatchSize != 10 && s.BatchSize != 20 && s.BatchSize != 30 {
		return Bad("invalid_batch_size")
	}
	switch s.StudyMode {
	case "all", "unlearned", "learned", "stale":
	default:
		return Bad("invalid_study_mode")
	}
	if s.LastBatchIndex < -1 {
		return Bad("invalid_batch_index")
	}
	return nil
}
func ValidateTrainingSettings(e *Exam, p Params, current TrainingSettings, input json.RawMessage) (TrainingSettings, error) {
	if current.BatchSize == 0 {
		current = DefaultTrainingSettings()
	}
	var in map[string]json.RawMessage
	if err := auxDecode(input, &in); err != nil {
		return current, err
	}
	allowed := map[string]bool{"batch_size": true, "last_batch_index": true, "study_mode": true, "last_part_id": true}
	for key := range in {
		if !allowed[key] {
			return current, Bad("invalid_payload")
		}
	}
	b, _ := json.Marshal(current)
	var merged map[string]json.RawMessage
	_ = json.Unmarshal(b, &merged)
	for k, v := range in {
		merged[k] = v
	}
	b, _ = json.Marshal(merged)
	var settings TrainingSettings
	if err := auxDecode(b, &settings); err != nil {
		return current, err
	}
	if err := trainingSettingsValid(settings); err != nil {
		return current, err
	}
	if _, present := in["last_part_id"]; !present {
		settings.LastPartID = nil
		if p.PartID != 0 {
			id := p.PartID
			settings.LastPartID = &id
		}
	}

	if settings.LastPartID != nil {
		found := false
		for _, part := range e.Parts {
			if part.ID == *settings.LastPartID {
				found = true
			}
		}
		if !found {
			return current, Missing
		}
	}
	foundPart := p.PartID == 0
	count := 0
	for _, part := range e.Parts {
		if part.ID == p.PartID {
			foundPart = true
		}
	}
	if !foundPart {
		return current, Missing
	}
	for _, q := range e.Questions {
		if (p.PartID == 0 || q.PartID == p.PartID) && strings.TrimSpace(q.Text) != "" && strings.TrimSpace(q.Answer) != "" {
			count++
		}
	}
	if count == 0 {
		return current, Conflict("exam_empty")
	}
	batches := (count + settings.BatchSize - 1) / settings.BatchSize
	if settings.LastBatchIndex >= batches {
		settings.LastBatchIndex = 0
	}
	return settings, nil
}
func TrainingView(e *Exam, a Actor, p Params, progress map[string]string, settings TrainingSettings) (any, error) {
	d, err := trainingScope(e, a, p)
	if err != nil {
		return nil, err
	}
	preview := p.Query["preview"] == "true" || p.Query["preview"] == "1"
	if preview {
		settings = DefaultTrainingSettings()
		progress = nil
	}
	if settings.BatchSize == 0 {
		settings = DefaultTrainingSettings()
	}
	if err := trainingSettingsValid(settings); err != nil {
		return nil, err
	}
	parts := map[int64]Part{}
	for _, part := range e.Parts {
		parts[part.ID] = part
	}
	all := []TrainingCard{}
	selected := []TrainingCard{}
	for _, part := range e.Parts {
		for _, q := range e.Questions {
			if q.PartID != part.ID || strings.TrimSpace(q.Text) == "" || strings.TrimSpace(q.Answer) == "" {
				continue
			}
			fp := TrainingFingerprint(q.Text, q.Answer)
			ref := trainingCardRef(e, q)
			status := "unlearned"
			if old, ok := progress[ref]; ok {
				if old == fp {
					status = "learned"
				} else {
					status = "answer_changed"
				}
			}
			card := TrainingCard{ref, q.ID, e.ID, q.PartID, part.Code, q.Text, q.Answer, fp, status}
			all = append(all, card)
			if p.PartID == 0 || q.PartID == p.PartID {
				selected = append(selected, card)
			}
		}
	}
	if len(all) == 0 && !preview {
		return nil, Conflict("exam_empty")
	}
	batches := []TrainingBatch{}
	for i := 0; i < len(selected); i += settings.BatchSize {
		end := i + settings.BatchSize
		if end > len(selected) {
			end = len(selected)
		}
		batches = append(batches, TrainingBatch{len(batches), i + 1, end, end - i, trainingStats(selected[i:end])})
	}
	if settings.LastBatchIndex >= len(batches) && settings.LastBatchIndex != -1 {
		settings.LastBatchIndex = 0
	}
	if settings.LastPartID != nil {
		if _, ok := parts[*settings.LastPartID]; !ok {
			zero := int64(0)
			settings.LastPartID = &zero
		}
	}
	partNodes := []any{}
	for _, part := range e.Parts {
		partCards := []TrainingCard{}
		for _, card := range all {
			if card.PartID == part.ID {
				partCards = append(partCards, card)
			}
		}
		partNodes = append(partNodes, map[string]any{"part_id": part.ID, "part_code": part.Code, "ref_id": fmt.Sprintf("%d:%d", e.ID, part.ID), "stats": trainingStats(partCards)})
	}
	ref := fmt.Sprint(e.ID)
	if p.PartID != 0 {
		ref += fmt.Sprintf(":%d", p.PartID)
	}
	return map[string]any{"success": true, "account_id": a.AccountID, "organization_id": e.OrganizationID, "division_id": p.DivisionID, "section_kind": "exam", "section_ref_id": ref, "exam": map[string]any{"id": e.ID, "exam_type": e.Kind, "direction_id": e.DirectionID, "training_enabled": d.TrainingEnabled, "start_at": d.Config.StartAt, "end_at": d.Config.EndAt, "version": e.Revision}, "parts": partNodes, "part_id": p.PartID, "cards": selected, "stats": trainingStats(selected), "exam_stats": trainingStats(all), "batches": batches, "settings": settings, "preview": preview}, nil
}

// ResolveTrainingCard validates the live card and optionally its optimistic
// fingerprint. Empty fingerprint supports removal without reconfirming content.
func ResolveTrainingCard(e *Exam, p Params, questionID int64, fingerprint string) (string, error) {
	if e.Kind != "classic" {
		return "", Bad("wrong_exam_type")
	}
	d, err := e.Division(p.DivisionID)
	if err != nil {
		return "", err
	}
	if !d.TrainingEnabled {
		return "", Conflict("training_disabled")
	}
	for _, q := range e.Questions {
		if q.ID != questionID || (p.PartID != 0 && q.PartID != p.PartID) {
			continue
		}
		if strings.TrimSpace(q.Text) == "" || strings.TrimSpace(q.Answer) == "" {
			return "", Missing
		}
		found := false
		for _, part := range e.Parts {
			if part.ID == q.PartID {
				found = true
			}
		}
		if !found {
			return "", Missing
		}
		current := TrainingFingerprint(q.Text, q.Answer)
		if fingerprint != "" && fingerprint != current {
			return "", Conflict("content_changed")
		}
		return current, nil
	}
	return "", Missing
}
