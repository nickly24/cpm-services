package tests

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// Uploads owns the file, preview session and workflow. Tests validates these
// domain inputs statelessly and creates one real test for a confirmed command.
type JSONImportPreviewDTO struct {
	OrganizationID int64           `json:"organization_id"`
	DivisionID     int64           `json:"division_id"`
	DirectionID    int64           `json:"direction_id"`
	Payload        json.RawMessage `json:"payload"`
}
type JSONImportCommitDTO struct {
	JSONImportPreviewDTO
	CommandID   string `json:"command_id"`
	PreviewHash string `json:"preview_hash"`
}
type JSONImportIssue struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}
type JSONImportedTest struct {
	Title         string     `json:"title"`
	DirectionID   int64      `json:"direction_id"`
	DirectionName string     `json:"direction_name"`
	Questions     []Question `json:"questions"`
	Settings      Settings   `json:"settings"`
}
type JSONImportSummary struct {
	QuestionsTotal int `json:"questions_total"`
	TotalPoints    int `json:"total_points"`
	SingleCount    int `json:"single_count"`
	MultipleCount  int `json:"multiple_count"`
	TextCount      int `json:"text_count"`
	ErrorsCount    int `json:"errors_count"`
}
type JSONImportPreview struct {
	Valid          bool              `json:"valid"`
	OrganizationID int64             `json:"organization_id"`
	DivisionID     int64             `json:"division_id"`
	DirectionID    int64             `json:"direction_id"`
	PreviewHash    string            `json:"preview_hash"`
	Preview        JSONImportedTest  `json:"preview"`
	Summary        JSONImportSummary `json:"summary"`
	Errors         []JSONImportIssue `json:"errors"`
	Source         string            `json:"source"`
	SourceTitle    string            `json:"source_title"`
	Warnings       []string          `json:"warnings"`
	OwnerProof     *ImportOwnerProof `json:"owner_proof,omitempty"`
}
type JSONImportCommitResult struct {
	CommandID string   `json:"command_id"`
	Test      TestView `json:"test"`
}

func jsonImportIssue(issues *[]JSONImportIssue, path, message string) {
	*issues = append(*issues, JSONImportIssue{path, message})
}
func jsonImportSpace(r rune) bool { return unicode.IsSpace(r) || r >= 0x1c && r <= 0x1f }
func jsonImportString(value any) string {
	if text, ok := value.(string); ok {
		return strings.TrimFunc(text, jsonImportSpace)
	}
	return ""
}
func jsonImportInt(value any) (int64, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	v, e := strconv.ParseInt(string(number), 10, 64)
	return v, e == nil
}
func jsonImportFloat(value any) (float64, bool) {
	var text string
	switch n := value.(type) {
	case json.Number:
		text = string(n)
	case string:
		text = strings.TrimSpace(n)
	default:
		return 0, false
	}
	v, e := strconv.ParseFloat(text, 64)
	return v, e == nil && !math.IsNaN(v) && !math.IsInf(v, 0)
}
func jsonImportFloatText(value float64) string {
	// Python str(int(-0.0)) is "0". The legacy OTP converter discards
	// numeric zero candidates before trying ValueText/HTML fallbacks.
	if value == 0 {
		return "0"
	}
	if value == math.Trunc(value) {
		return strconv.FormatFloat(value, 'f', 0, 64)
	}
	return strconv.FormatFloat(value, 'g', -1, 64)
}
func jsonImportValueString(value any) string {
	switch n := value.(type) {
	case string:
		return jsonImportString(n)
	case json.Number:
		if !strings.ContainsAny(string(n), ".eE") {
			if n == "-0" {
				return "0"
			}
			return string(n)
		}
		if v, ok := jsonImportFloat(n); ok {
			return jsonImportFloatText(v)
		}
	}
	return ""
}
func jsonImportContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
func jsonImportTruthy(v any) bool {
	switch n := v.(type) {
	case nil:
		return false
	case bool:
		return n
	case string:
		return n != ""
	case json.Number:
		f, ok := jsonImportFloat(n)
		return ok && f != 0
	case []any:
		return len(n) > 0
	case map[string]any:
		return len(n) > 0
	}
	return true
}
func jsonImportBool(value any, path string, fallback bool, issues *[]JSONImportIssue) bool {
	if value == nil {
		return fallback
	}
	if v, ok := value.(bool); ok {
		return v
	}
	jsonImportIssue(issues, path, "Значение должно быть boolean: true или false")
	return fallback
}

var jsonImportDatePattern = regexp.MustCompile(`^(\d{4})-(\d{1,2})-(\d{1,2})T(\d{1,2}):(\d{1,2})$`)

func jsonImportDate(value any, path string, issues *[]JSONImportIssue) *time.Time {
	text := jsonImportString(value)
	if text == "" {
		jsonImportIssue(issues, path, "Поле обязательно")
		return nil
	}
	match := jsonImportDatePattern.FindStringSubmatch(text)
	if match == nil {
		jsonImportIssue(issues, path, "Используйте формат YYYY-MM-DDTHH:mm")
		return nil
	}
	n := [5]int{}
	for i := range n {
		n[i], _ = strconv.Atoi(match[i+1])
	}
	moscow, e := time.LoadLocation("Europe/Moscow")
	if e != nil {
		jsonImportIssue(issues, path, "Недоступна временная зона Europe/Moscow")
		return nil
	}
	t := time.Date(n[0], time.Month(n[1]), n[2], n[3], n[4], 0, 0, moscow)
	if n[0] < 1 || t.Year() != n[0] || int(t.Month()) != n[1] || t.Day() != n[2] || t.Hour() != n[3] || t.Minute() != n[4] {
		jsonImportIssue(issues, path, "Используйте формат YYYY-MM-DDTHH:mm")
		return nil
	}
	t = t.UTC()
	return &t
}

var jsonImportBreakTags = regexp.MustCompile(`(?i)<\s*br\s*/?\s*>`)
var jsonImportClosingTags = regexp.MustCompile(`(?i)</\s*(p|div|li|tr|h[1-6])\s*>`)
var jsonImportHTMLTags = regexp.MustCompile(`<[^>]+>`)

func jsonImportHTML(value any) string {
	text := jsonImportString(value)
	text = jsonImportBreakTags.ReplaceAllString(text, "\n")
	text = jsonImportClosingTags.ReplaceAllString(text, "\n")
	text = html.UnescapeString(jsonImportHTMLTags.ReplaceAllString(text, ""))
	lines := strings.FieldsFunc(text, func(r rune) bool {
		return r == '\n' || r == '\r' || r == '\v' || r == '\f' || r >= 0x1c && r <= 0x1e || r == 0x85 || r == 0x2028 || r == 0x2029
	})
	clean := []string{}
	for _, line := range lines {
		line = strings.Join(strings.FieldsFunc(line, jsonImportSpace), " ")
		if line != "" {
			clean = append(clean, line)
		}
	}
	return strings.Join(clean, "\n")
}
func jsonImportOptionID(index int) string {
	out := ""
	for {
		out = string(rune('a'+index%26)) + out
		index = index/26 - 1
		if index < 0 {
			return out
		}
	}
}

var jsonImportOTPMarker = regexp.MustCompile(`\bvar\s+test\s*=`)

func jsonImportOTPSource(value any, issues *[]JSONImportIssue) map[string]any {
	text := strings.TrimLeft(jsonImportString(value), "\ufeff")
	if text == "" {
		jsonImportIssue(issues, "sourceText", "Передайте содержимое файла test.js")
		return map[string]any{}
	}
	match := jsonImportOTPMarker.FindStringIndex(text)
	if match == nil {
		jsonImportIssue(issues, "sourceText", "Не найден объект var test = {...}")
		return map[string]any{}
	}
	start := strings.Index(text[match[1]:], "{")
	if start < 0 {
		jsonImportIssue(issues, "sourceText", "Не найден JSON-объект test")
		return map[string]any{}
	}
	start += match[1]
	depth, inString, escaped, end := 0, false, false, -1
	for i := start; i < len(text); i++ {
		c := text[i]
		if inString {
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				end = i + 1
			}
		}
		if end >= 0 {
			break
		}
	}
	if end < 0 {
		jsonImportIssue(issues, "sourceText", "Не удалось определить конец объекта test")
		return map[string]any{}
	}
	out, e := jsonImportObject(json.RawMessage(text[start:end]))
	if e != nil {
		jsonImportIssue(issues, "sourceText", "test.js содержит некорректный JSON")
		return map[string]any{}
	}
	return out
}
func jsonImportObject(raw json.RawMessage) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value any
	if e := dec.Decode(&value); e != nil {
		return nil, e
	}
	if e := dec.Decode(new(any)); e != io.EOF {
		return nil, errors.New("invalid JSON")
	}
	out, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("test must be an object")
	}
	return out, nil
}
func jsonImportOTPSorted(items []any) []any {
	out := append([]any{}, items...)
	ordinal := func(item any) int64 {
		m, _ := item.(map[string]any)
		if v, ok := m["Number"].(bool); ok {
			if v {
				return 1
			}
			return 0
		}
		v, _ := jsonImportInt(m["Number"])
		return v
	}
	sort.SliceStable(out, func(i, j int) bool { return ordinal(out[i]) < ordinal(out[j]) })
	return out
}
func jsonImportOTPCorrect(answer map[string]any, useScore bool) bool {
	if useScore {
		return jsonImportOTPPositiveScore(answer["Score"])
	}
	return answer["IsRightAnswer"] == true
}
func jsonImportOTPPositiveScore(value any) bool {
	if v, ok := value.(bool); ok {
		return v
	}
	var raw string
	switch v := value.(type) {
	case json.Number:
		raw = string(v)
	case string:
		raw = strings.TrimSpace(v)
	default:
		return false
	}
	parsed, _ := strconv.ParseFloat(raw, 64)
	return parsed > 0
}

var jsonImportTextVariants = regexp.MustCompile(`(?i)\s*<или>\s*`)

func jsonImportVariants(text string) []string {
	out := []string{}
	for _, value := range jsonImportTextVariants.Split(text, -1) {
		value = jsonImportString(value)
		if value != "" && !jsonImportContains(out, value) {
			out = append(out, value)
		}
	}
	return out
}
func jsonImportOTPText(answer map[string]any, kind int) []string {
	if kind == 410 {
		out := []string{}
		if v := answer["ValueInt"]; v != nil && v != "" {
			text := jsonImportValueString(v)
			if text != "" && text != "0" {
				out = append(out, text)
			}
		}
		if v := answer["ValueFloat"]; v != nil && v != "" {
			text := jsonImportValueString(v)
			if f, ok := jsonImportFloat(v); ok {
				text = jsonImportFloatText(f)
			}
			if text != "" && text != "0" && !jsonImportContains(out, text) {
				out = append(out, text)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	if text := jsonImportString(answer["ValueText"]); text != "" {
		return jsonImportVariants(text)
	}
	text := jsonImportHTML(answer["TextWOHtml"])
	if text == "" {
		text = jsonImportHTML(answer["Text"])
	}
	return jsonImportVariants(text)
}
func jsonImportOTPQuestion(value any, index int, issues *[]JSONImportIssue) map[string]any {
	path := fmt.Sprintf("Questions[%d]", index)
	q, ok := value.(map[string]any)
	if !ok {
		jsonImportIssue(issues, path, "Вопрос должен быть объектом")
		q = map[string]any{}
	}
	kind, _ := jsonImportFloat(q["AnswerType"])
	typeName := "single"
	switch kind {
	case 10:
	case 210:
		typeName = "multiple"
	case 410, 420:
		typeName = "text"
	default:
		jsonImportIssue(issues, path+".AnswerType", "Неизвестный AnswerType")
	}
	rawPoints := q["maxScore"]
	if !jsonImportTruthy(rawPoints) {
		rawPoints = q["RightScore"]
	}
	points := 1.0
	if f, ok := jsonImportFloat(rawPoints); ok {
		points = math.Max(1, math.RoundToEven(f))
	}
	if points > 100000 {
		jsonImportIssue(issues, path+".maxScore", "Вес вопроса превышает допустимый предел")
	}
	rawTemplates := q["AnswerTemplates"]
	templates, ok := rawTemplates.([]any)
	if !ok && jsonImportTruthy(rawTemplates) {
		jsonImportIssue(issues, path+".AnswerTemplates", "AnswerTemplates должен быть массивом")
	}
	templates = jsonImportOTPSorted(templates)
	useScore := false
	for _, v := range templates {
		if a, ok := v.(map[string]any); ok {
			if jsonImportOTPPositiveScore(a["Score"]) {
				useScore = true
			}
		}
	}
	out := map[string]any{"questionId": json.Number(strconv.Itoa(index + 1)), "type": typeName, "text": jsonImportHTML(q["Text"]), "points": json.Number(strconv.FormatFloat(points, 'f', 0, 64)), "answers": []any{}}
	if typeName != "text" {
		answers := []any{}
		for i, v := range templates {
			a, ok := v.(map[string]any)
			if !ok {
				jsonImportIssue(issues, fmt.Sprintf("%s.AnswerTemplates[%d]", path, i), "Вариант ответа должен быть объектом")
				continue
			}
			text := jsonImportHTML(a["TextWOHtml"])
			if text == "" {
				text = jsonImportHTML(a["Text"])
			}
			if text == "" {
				text = jsonImportString(a["ValueText"])
			}
			if text == "" {
				text = jsonImportValueString(a["ValueInt"])
			}
			if text == "" {
				text = jsonImportValueString(a["ValueFloat"])
			}
			answers = append(answers, map[string]any{"id": jsonImportOptionID(i), "text": text, "isCorrect": jsonImportOTPCorrect(a, useScore)})
		}
		out["answers"] = answers
	} else {
		answers := []any{}
		seen := map[string]bool{}
		for _, v := range templates {
			if a, ok := v.(map[string]any); ok && jsonImportOTPCorrect(a, useScore) {
				for _, text := range jsonImportOTPText(a, int(kind)) {
					if !seen[text] {
						answers = append(answers, text)
						seen[text] = true
					}
				}
			}
		}
		if len(answers) == 0 {
			jsonImportIssue(issues, path+".AnswerTemplates", "Для text-вопроса не найден правильный ответ")
		}
		out["correctAnswers"] = answers
	}
	return out
}
func jsonImportConvertOTP(payload map[string]any, issues *[]JSONImportIssue) (map[string]any, string, []string) {
	source := jsonImportOTPSource(payload["sourceText"], issues)
	metadata, _ := payload["metadata"].(map[string]any)
	params, _ := source["Params"].(map[string]any)
	rawQuestions, ok := source["Questions"].([]any)
	if len(source) > 0 && !ok {
		jsonImportIssue(issues, "Questions", "В test.js нет массива Questions")
	}
	if len(source) > 0 && len(rawQuestions) == 0 {
		jsonImportIssue(issues, "Questions", "Добавьте хотя бы один вопрос")
	}
	questions := []any{}
	for i, q := range jsonImportOTPSorted(rawQuestions) {
		questions = append(questions, jsonImportOTPQuestion(q, i, issues))
	}
	limit := metadata["timeLimitMinutes"]
	if limit == nil {
		if params["timelimited"] == true {
			limit = params["timelimitminutes"]
		} else {
			limit = json.Number("30")
		}
	}
	title, exists := metadata["title"]
	if !exists {
		title = source["Name"]
	}
	published, exists := metadata["published"]
	if !exists {
		published = false
	}
	visible, exists := metadata["visible"]
	if !exists {
		visible = false
	}
	warnings := []string{}
	if source["IsLimitedQuestions"] == true {
		count := jsonImportValueString(source["LimitedQuestionsCount"])
		suffix := ""
		if jsonImportTruthy(source["LimitedQuestionsCount"]) {
			suffix = " (" + count + ")"
		}
		warnings = append(warnings, "В исходном тесте включён лимит случайных вопросов"+suffix+"; CPM v1 импортирует все вопросы.")
	}
	if source["IsRandomQuestions"] == true {
		warnings = append(warnings, "В исходном тесте включён случайный порядок вопросов; CPM перемешивает вопросы при старте попытки.")
	}
	if source["IsRandomAnswersInAllQuestions"] == true {
		warnings = append(warnings, "В исходном тесте включено перемешивание ответов; CPM v1 сохраняет порядок вариантов из файла.")
	}
	return map[string]any{"title": title, "direction": metadata["direction"], "startDate": metadata["startDate"], "endDate": metadata["endDate"], "timeLimitMinutes": limit, "published": published, "visible": visible, "questions": questions}, jsonImportString(source["Name"]), warnings
}

func jsonImportNormalizeQuestion(value any, index int, seen map[int64]bool, issues *[]JSONImportIssue) Question {
	path := fmt.Sprintf("questions[%d]", index)
	m, ok := value.(map[string]any)
	if !ok {
		jsonImportIssue(issues, path, "Вопрос должен быть объектом")
		m = map[string]any{}
	}
	id, ok := jsonImportInt(m["questionId"])
	if !ok || id < 1 {
		jsonImportIssue(issues, path+".questionId", "questionId должен быть целым числом больше 0")
		id = int64(index + 1)
	} else {
		if seen[id] {
			jsonImportIssue(issues, path+".questionId", "questionId повторяется")
		}
		seen[id] = true
	}
	kind := jsonImportString(m["type"])
	if kind != "single" && kind != "multiple" && kind != "text" {
		jsonImportIssue(issues, path+".type", "Тип должен быть single, multiple или text")
		kind = "single"
	}
	text := jsonImportString(m["text"])
	if text == "" {
		jsonImportIssue(issues, path+".text", "Текст вопроса обязателен")
	}
	points, ok := jsonImportInt(m["points"])
	if !ok || points < 1 {
		jsonImportIssue(issues, path+".points", "Баллы должны быть целым числом больше 0")
		points = 1
	} else if points > 100000 {
		jsonImportIssue(issues, path+".points", "Вес вопроса превышает допустимый предел")
		points = 1
	}
	q := Question{QuestionID: id, Type: kind, Text: text, Points: int(points), Answers: []AnswerOption{}, CorrectAnswers: []string{}}
	if kind == "text" {
		if v := m["answers"]; v != nil {
			values, ok := v.([]any)
			if !ok || len(values) > 0 {
				jsonImportIssue(issues, path+".answers", "Для text поле answers должно быть пустым")
			}
		}
		aliases, ok := m["correctAnswers"].([]any)
		if !ok {
			jsonImportIssue(issues, path+".correctAnswers", "correctAnswers должен быть массивом")
		}
		for _, value := range aliases {
			if alias := jsonImportString(value); alias != "" {
				q.CorrectAnswers = append(q.CorrectAnswers, alias)
			}
		}
		if len(q.CorrectAnswers) == 0 {
			jsonImportIssue(issues, path+".correctAnswers", "Добавьте минимум один правильный ответ")
		}
	} else {
		answers, ok := m["answers"].([]any)
		if !ok {
			jsonImportIssue(issues, path+".answers", "answers должен быть массивом")
		}
		if len(answers) < 2 {
			jsonImportIssue(issues, path+".answers", "Добавьте минимум 2 варианта ответа")
		}
		options := map[string]bool{}
		correct := 0
		for i, value := range answers {
			p := fmt.Sprintf("%s.answers[%d]", path, i)
			answer, ok := value.(map[string]any)
			if !ok {
				jsonImportIssue(issues, p, "Вариант ответа должен быть объектом")
				answer = map[string]any{}
			}
			id, text := jsonImportString(answer["id"]), jsonImportString(answer["text"])
			if id == "" {
				jsonImportIssue(issues, p+".id", "ID варианта обязателен")
			} else if options[id] {
				jsonImportIssue(issues, p+".id", "ID варианта повторяется")
			}
			options[id] = true
			if text == "" {
				jsonImportIssue(issues, p+".text", "Текст варианта обязателен")
			}
			right, ok := answer["isCorrect"].(bool)
			if !ok {
				jsonImportIssue(issues, p+".isCorrect", "isCorrect должен быть boolean")
			}
			if right {
				correct++
			}
			q.Answers = append(q.Answers, AnswerOption{ID: id, Text: text, IsCorrect: right})
		}
		if kind == "single" && correct != 1 {
			jsonImportIssue(issues, path+".answers", "Для single должен быть ровно один правильный ответ")
		}
		if kind == "multiple" && correct < 1 {
			jsonImportIssue(issues, path+".answers", "Для multiple нужен минимум один правильный ответ")
		}
	}
	return q
}

func buildJSONImportPreview(in JSONImportPreviewDTO, directionName string) JSONImportPreview {
	out := JSONImportPreview{OrganizationID: in.OrganizationID, DivisionID: in.DivisionID, DirectionID: in.DirectionID, Source: "json", Errors: []JSONImportIssue{}, Warnings: []string{}, Preview: JSONImportedTest{DirectionID: in.DirectionID, DirectionName: directionName, Questions: []Question{}}}
	payload, e := jsonImportObject(in.Payload)
	if e != nil {
		jsonImportIssue(&out.Errors, "$", "JSON должен быть объектом теста")
		payload = map[string]any{}
	}
	if payload["importFormat"] == "online_test_pad" {
		out.Source = "online_test_pad"
		payload, out.SourceTitle, out.Warnings = jsonImportConvertOTP(payload, &out.Errors)
	}
	if payload["importFormat"] == "question_table" {
		out.Source = "question_table"
		payload, out.SourceTitle, out.Warnings = jsonImportConvertTable(payload, &out.Errors)
	}
	out.Preview.Title = jsonImportString(payload["title"])
	if out.Preview.Title == "" {
		jsonImportIssue(&out.Errors, "title", "Название теста обязательно")
	}
	if name := jsonImportString(payload["direction"]); name != "" && name != directionName {
		jsonImportIssue(&out.Errors, "direction", "Направление из файла не совпадает с выбранным направлением организации")
	}
	settings := Settings{Published: jsonImportBool(payload["published"], "published", true, &out.Errors), AnswersVisible: jsonImportBool(payload["visible"], "visible", false, &out.Errors)}
	settings.StartAt = jsonImportDate(payload["startDate"], "startDate", &out.Errors)
	settings.EndAt = jsonImportDate(payload["endDate"], "endDate", &out.Errors)
	if settings.StartAt != nil && settings.EndAt != nil && !settings.EndAt.After(*settings.StartAt) {
		jsonImportIssue(&out.Errors, "endDate", "Дата окончания должна быть позже даты начала")
	}
	limit, ok := jsonImportInt(payload["timeLimitMinutes"])
	if !ok || limit < 1 {
		jsonImportIssue(&out.Errors, "timeLimitMinutes", "Время должно быть целым числом больше 0")
		limit = 30
	}
	settings.TimeLimitMinutes = int(limit)
	out.Preview.Settings = settings
	rawQuestions, ok := payload["questions"].([]any)
	if !ok {
		jsonImportIssue(&out.Errors, "questions", "questions должен быть массивом")
	}
	if len(rawQuestions) == 0 {
		jsonImportIssue(&out.Errors, "questions", "Добавьте хотя бы один вопрос")
	}
	seen := map[int64]bool{}
	for i, raw := range rawQuestions {
		q := jsonImportNormalizeQuestion(raw, i, seen, &out.Errors)
		out.Preview.Questions = append(out.Preview.Questions, q)
		out.Summary.TotalPoints += q.Points
		switch q.Type {
		case "single":
			out.Summary.SingleCount++
		case "multiple":
			out.Summary.MultipleCount++
		case "text":
			out.Summary.TextCount++
		}
	}
	if e := validateMetadata(CreateDTO{Title: out.Preview.Title, DirectionID: in.DirectionID}); e != nil {
		jsonImportIssue(&out.Errors, "title", "Название или направление не соответствует ограничениям Tests")
	}
	if e := validateSettings(settings); e != nil {
		jsonImportIssue(&out.Errors, "settings", e.Error())
	}
	if e := validateQuestions(out.Preview.Questions, false); e != nil {
		jsonImportIssue(&out.Errors, "questions", e.Error())
	}
	out.Summary.QuestionsTotal = len(out.Preview.Questions)
	out.Summary.ErrorsCount = len(out.Errors)
	out.Valid = len(out.Errors) == 0
	if out.SourceTitle == "" {
		out.SourceTitle = out.Preview.Title
	}
	sum := sha256.Sum256(canonicalJSON(out))
	out.PreviewHash = hex.EncodeToString(sum[:])
	out.OwnerProof = jsonImportOwnerProof(in, out.PreviewHash)
	return out
}

func (s *Server) jsonImportAccess(r *http.Request, a Actor, in JSONImportPreviewDTO, pathDivision int64) (References, error) {
	if !a.Can(true) || !actorUpload(a, true) || in.OrganizationID != a.OrganizationID || in.DivisionID < 1 || !a.InDivision(in.DivisionID) || pathDivision > 0 && pathDivision != in.DivisionID {
		return References{}, denied
	}
	if in.DirectionID < 1 || len(in.Payload) == 0 || len(in.Payload) > 900000 {
		return References{}, bad("invalid_json_import_request")
	}
	refs, e := s.Dependencies.References(r.Context(), in.OrganizationID, []int64{in.DivisionID}, &in.DirectionID)
	if e != nil {
		return refs, e
	}
	if !refs.Divisions[0].Active {
		return refs, missing
	}
	if !refs.Direction.Active {
		return refs, conflict("direction_archived")
	}
	return refs, nil
}
func (s *Server) jsonImportPreview(r *http.Request, a Actor, division int64) (any, int, error) {
	var in JSONImportPreviewDTO
	if e := decode(r, &in); e != nil {
		return nil, 0, e
	}
	if e := checkJSONImportBinding(r, a, in, "", ""); e != nil {
		return nil, 0, e
	}
	refs, e := s.jsonImportAccess(r, a, in, division)
	if e != nil {
		return nil, 0, e
	}
	preview := buildJSONImportPreview(in, refs.Direction.Name)
	if e := checkImportOwnerProof(r, preview.OwnerProof); e != nil {
		return nil, 0, e
	}
	return preview, http.StatusOK, nil
}
func jsonImportOperation(r *http.Request, a Actor, in JSONImportCommitDTO) (bson.ObjectID, string, error) {
	command := strings.ToLower(strings.TrimSpace(in.CommandID))
	digest, e := hex.DecodeString(in.PreviewHash)
	if !externalImportValidUUID(command) || e != nil || len(digest) != sha256.Size || !json.Valid(in.Payload) {
		return bson.NilObjectID, "", bad("invalid_json_import_commit")
	}
	req := r.Clone(r.Context())
	req.Method = http.MethodPost
	u := *r.URL
	u.Path = fmt.Sprintf("/v1/tests/divisions/%d/imports/json/commit", in.DivisionID)
	req.URL = &u
	req.Header = r.Header.Clone()
	req.Header.Set("Idempotency-Key", "json-import-"+command)
	payload := struct {
		OrganizationID, DivisionID, DirectionID int64
		PreviewHash                             string
		Payload                                 json.RawMessage
	}{in.OrganizationID, in.DivisionID, in.DirectionID, strings.ToLower(in.PreviewHash), json.RawMessage(canonicalJSON(in.Payload))}
	return operation(req, a, payload)
}
func (s *Server) jsonImportCommit(r *http.Request, a Actor, division int64) (any, int, error) {
	var in JSONImportCommitDTO
	if e := decode(r, &in); e != nil {
		return nil, 0, e
	}
	in.CommandID = strings.ToLower(strings.TrimSpace(in.CommandID))
	id, hash, e := jsonImportOperation(r, a, in)
	if e != nil {
		return nil, 0, e
	}
	if e := checkJSONImportBinding(r, a, in.JSONImportPreviewDTO, in.CommandID, hash); e != nil {
		return nil, 0, e
	}
	var refs References
	if importOperationContext(r) == nil {
		refs, e = s.jsonImportAccess(r, a, in.JSONImportPreviewDTO, division)
		if e != nil {
			return nil, 0, e
		}
	} else if !a.Can(true) || !actorUpload(a, true) || !a.InDivision(in.DivisionID) || in.OrganizationID != a.OrganizationID {
		return nil, 0, denied
	}
	// Check the durable operation before normalizing/recreating a version. A
	// changed invalid replay must conflict without producing any owner writes.
	old, e := s.Store.Get(r.Context(), a.OrganizationID, id)
	if e == nil {
		if old.OperationHash != hash || old.OwnerDivisionID != in.DivisionID {
			return nil, 0, conflict("idempotency_conflict")
		}
		if !old.Editable(a) || importOperationContext(r) != nil && old.CreatedBy != a.AccountID {
			return nil, 0, denied
		}
		view, e := s.view(r, a, old, in.DivisionID)
		return JSONImportCommitResult{CommandID: strings.ToLower(in.CommandID), Test: view}, http.StatusOK, e
	}
	if !errors.Is(e, missing) {
		return nil, 0, e
	}
	if importOperationContext(r) != nil {
		refs, e = s.jsonImportAccess(r, a, in.JSONImportPreviewDTO, division)
		if e != nil {
			return nil, 0, e
		}
	}
	preview := buildJSONImportPreview(in.JSONImportPreviewDTO, refs.Direction.Name)
	if !preview.Valid {
		return nil, 0, bad("json_import_validation_failed")
	}
	if preview.PreviewHash != strings.ToLower(in.PreviewHash) {
		return nil, 0, conflict("json_import_preview_changed")
	}
	if e := checkImportOwnerProof(r, preview.OwnerProof); e != nil {
		return nil, 0, e
	}
	now := time.Now().UTC()
	normalized := preview.Preview
	doc := Document{ID: id, OrganizationID: a.OrganizationID, OwnerDivisionID: in.DivisionID, DirectionID: in.DirectionID, Title: normalized.Title, Revision: 1,
		Placements: []Placement{{DivisionID: in.DivisionID, Settings: normalized.Settings, AddedBy: a.AccountID, AddedAt: now, UpdatedBy: a.AccountID, UpdatedAt: now}},
		CreatedBy:  a.AccountID, CreatedAt: now, UpdatedBy: a.AccountID, UpdatedAt: now, OperationHash: hash}
	if e = s.initialContent(r.Context(), &doc, normalized.Questions, a); e != nil {
		return nil, 0, e
	}
	doc, repeated, e := s.Store.Insert(r.Context(), doc)
	if e != nil {
		return nil, 0, e
	}
	view, e := s.view(r, a, doc, in.DivisionID)
	code := http.StatusCreated
	if repeated {
		code = http.StatusOK
	}
	return JSONImportCommitResult{CommandID: strings.ToLower(in.CommandID), Test: view}, code, e
}
func (s *Server) jsonImportValidateInternal(r *http.Request) (any, int, error) {
	a, e := s.identifyImport(r)
	if e != nil {
		return nil, 0, e
	}
	return s.jsonImportPreview(r, a, 0)
}
func (s *Server) jsonImportCommitInternal(r *http.Request) (any, int, error) {
	a, e := s.identifyImport(r)
	if e != nil {
		return nil, 0, e
	}
	return s.jsonImportCommit(r, a, 0)
}
