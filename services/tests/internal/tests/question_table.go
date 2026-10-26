package tests

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

type tableOption struct{ label, text string }

var tableMarker = regexp.MustCompile(`(?m)^[\t ]*([А-ЯЁа-яёA-Za-z]+|[0-9]+)[.)][\t ]*`)
var tableInline = regexp.MustCompile(`[\t \x{00a0}]{2,}([А-ЯЁа-яёA-Za-z]+|[0-9]+)[.)][\t \x{00a0}]+`)
var tableDate = regexp.MustCompile(`^([0-9]{1,2})[./]([0-9]{1,2})[./]([0-9]{4})$`)

func tableOptions(text string) ([]tableOption, bool) {
	text = strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(text), "\r\n", "\n"), "\r", "\n")
	text = tableInline.ReplaceAllString(text, "\n$1. ")
	marks := tableMarker.FindAllStringSubmatchIndex(text, -1)
	if len(marks) == 0 || strings.TrimSpace(text[:marks[0][0]]) != "" {
		return nil, false
	}
	options := []tableOption{}
	seen := map[string]bool{}
	for i, m := range marks {
		label := strings.ToUpper(text[m[2]:m[3]])
		// Alphabetic labels may span any number of letters (А, АМ, БД).
		if label[0] >= '0' && label[0] <= '9' {
			if strings.TrimLeft(label, "0") == "" {
				return nil, false
			}
		}
		end := len(text)
		if i+1 < len(marks) {
			end = marks[i+1][0]
		}
		body := strings.TrimSpace(text[m[1]:end])
		if seen[label] {
			return nil, false
		}
		seen[label] = true
		options = append(options, tableOption{label, body})
	}
	return options, true
}
func tableComparable(text string) string {
	return strings.TrimRight(strings.ToLower(strings.Join(strings.Fields(text), " ")), ".; ")
}

// The table carries raw cell values from Uploads. This owner converts them to
// the same CPM input and applies the existing limits, permissions and hashing.
func jsonImportConvertTable(payload map[string]any, issues *[]JSONImportIssue) (map[string]any, string, []string) {
	metadata, _ := payload["metadata"].(map[string]any)
	out := map[string]any{}
	for _, key := range []string{"title", "direction", "startDate", "endDate", "timeLimitMinutes", "published", "visible"} {
		out[key] = metadata[key]
	}
	if out["published"] == nil {
		out["published"] = false
	}
	if out["visible"] == nil {
		out["visible"] = false
	}
	warnings := []string{}
	questions := []any{}
	title := ""
	rows, ok := payload["rows"].([]any)
	if !ok || len(rows) == 0 {
		jsonImportIssue(issues, "rows", "В таблице должен быть хотя бы один вопрос")
		rows = nil
	}
	for i, value := range rows {
		row, ok := value.(map[string]any)
		if !ok {
			jsonImportIssue(issues, fmt.Sprintf("rows[%d]", i), "Некорректная строка таблицы")
			continue
		}
		number, _ := jsonImportInt(row["row"])
		prefix := fmt.Sprintf("Строка %d", number)
		p := fmt.Sprintf("rows[%d]", i)
		issue := func(field, message string) { jsonImportIssue(issues, p+"."+field, prefix+": "+message) }
		warn := func(message string) { warnings = append(warnings, prefix+": "+message) }
		if cells, ok := row["cell_errors"].(map[string]any); ok {
			for _, field := range []string{"title", "number", "category", "question", "options", "answer"} {
				if _, exists := cells[field]; exists {
					issue(field, "исправьте ошибку Excel или тип значения в ячейке")
				}
			}
		}

		sourceTitle := jsonImportString(row["title"])
		if title == "" {
			title = sourceTitle
		} else if sourceTitle != "" && sourceTitle != title {
			warn("название теста отличается от первой строки; используется общее название из настроек импорта")
		}
		rawID := jsonImportString(row["number"])
		id, err := strconv.ParseInt(rawID, 10, 64)
		if err != nil || id < 1 {
			issue("number", "номер вопроса должен быть положительным целым числом")
			id = int64(i + 1)
		}
		text := jsonImportString(row["question"])
		optionsText := jsonImportString(row["options"])
		keyText := jsonImportString(row["answer"])
		category := strings.ToLower(jsonImportString(row["category"]))
		q := map[string]any{"questionId": json.Number(strconv.FormatInt(id, 10)), "type": "text", "text": text, "points": json.Number("1"), "answers": []any{}, "correctAnswers": []any{}}
		if text == "" {
			issue("question", "отсутствует текст вопроса")
		}
		if keyText == "" {
			issue("answer", "отсутствует правильный ответ")
		}
		if optionsText == "" {
			aliases := []any{}
			seen := map[string]bool{}
			values := []string{}
			// A line is an alternative; slash also remains supported. Recognize
			// each date before splitting so 01/02/2026 stays a single answer.
			for _, line := range strings.Split(strings.ReplaceAll(strings.ReplaceAll(keyText, "\r\n", "\n"), "\r", "\n"), "\n") {
				if match := tableDate.FindStringSubmatch(strings.TrimSpace(line)); match != nil {
					values = append(values, match[1]+"."+match[2]+"."+match[3])
				} else {
					values = append(values, strings.Split(line, "/")...)
				}
			}
			for _, alias := range values {
				alias = strings.TrimSpace(alias)
				key := strings.ToLower(alias)
				if alias != "" && !seen[key] {
					aliases = append(aliases, alias)
					seen[key] = true
				}
			}
			q["correctAnswers"] = aliases
			if strings.Contains(category, "вариант") {
				warn("варианты отсутствуют: вопрос импортируется с текстовым ответом")
			}
		} else {
			opts, valid := tableOptions(optionsText)
			keys, keyValid := tableOptions(keyText)
			if !valid || len(opts) < 2 {
				issue("options", "нужны минимум два варианта с метками А., Б. и т. п.")
			}
			if !keyValid {
				issue("answer", "правильные варианты должны содержать метки, например А. Ответ")
			}
			known := map[string]string{}
			byText := map[string][]string{}
			for _, o := range opts {
				known[o.label] = o.text
				c := tableComparable(o.text)
				byText[c] = append(byText[c], o.label)
			}
			duplicate := false
			for _, labels := range byText {
				if len(labels) > 1 {
					duplicate = true
				}
			}
			if duplicate {
				warn("есть одинаковые тексты вариантов; метки и правильность сохранены, проверьте вопрос")
			}
			selected := map[string]bool{}
			for _, key := range keys {
				label := key.label
				expected, exists := known[label]
				matches := byText[tableComparable(key.text)]
				if key.text != "" && tableComparable(key.text) != tableComparable(expected) {
					if len(matches) == 1 {
						if matches[0] != label {
							warn("метка правильного ответа " + label + " исправлена на " + matches[0] + " по точному совпадению текста")
						}
						label = matches[0]
						exists = true
					} else if exists {
						warn("формулировка правильного ответа " + label + " отличается от варианта; использована метка, проверьте вопрос")
					} else {
						issue("answer", "не удалось однозначно сопоставить правильный ответ "+key.label)
						continue
					}
				}
				if !exists {
					issue("answer", "неизвестная метка правильного ответа "+key.label)
					continue
				}
				selected[label] = true
			}
			kind := "single"
			if len(selected) > 1 || strings.Contains(category, "нескольк") {
				kind = "multiple"
			}
			q["type"] = kind
			if kind == "multiple" && !strings.Contains(category, "нескольк") {
				warn("несколько правильных ответов: выбран тип с несколькими вариантами")
			}
			if !strings.Contains(category, "вариант") {
				warn("тип вопроса определён по вариантам и правильным ответам")
			}
			answers := []any{}
			for n, o := range opts {
				answers = append(answers, map[string]any{"id": fmt.Sprintf("opt_%d", n+1), "text": o.text, "isCorrect": selected[o.label]})
			}
			q["answers"] = answers
		}
		questions = append(questions, q)
	}
	if jsonImportString(out["title"]) == "" {
		out["title"] = title
	}
	out["questions"] = questions
	return out, title, warnings
}
