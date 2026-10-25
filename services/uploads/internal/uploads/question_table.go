package uploads

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

func questionImportMetadata(fields map[string]string) (map[string]any, error) {
	metadata := map[string]any{}
	for _, key := range []string{"title", "direction", "startDate", "endDate"} {
		if value, ok := fields[key]; ok {
			metadata[key] = value
		}
	}
	if value, ok := fields["timeLimitMinutes"]; ok {
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return nil, api(400, "invalid_source_metadata")
		}
		metadata["timeLimitMinutes"] = n
	}
	for _, key := range []string{"published", "visible"} {
		if value, ok := fields[key]; ok {
			if value != "true" && value != "false" {
				return nil, api(400, "invalid_source_metadata")
			}
			metadata[key] = value == "true"
		}
	}
	return metadata, nil
}

var questionTableAliases = map[string][]string{
	"title": {"название теста"}, "number": {"номер", "номер вопроса", "№"},
	"category": {"категория вопроса", "тип вопроса"}, "question": {"вопрос"},
	"options": {"варианты ответов", "варианты ответа"}, "answer": {"правильный ответ", "правильные ответы"},
}

// Extract only physical source cells. Tests owns interpretation, validation,
// answer matching and all writes; source tenant/direction names grant no access.
func questionTableInput(content []byte, fields map[string]string) (json.RawMessage, error) {
	records, err := sourceWorkbook(content, false, true)
	if err != nil {
		return nil, err
	}
	header := -1
	var columns map[string]int
	for i, row := range records {
		if row.number > 10 {
			break
		}
		mapping := sourceHeaders(row, questionTableAliases)
		complete := true
		for _, key := range []string{"title", "number", "category", "question", "options", "answer"} {
			if _, ok := mapping[key]; !ok {
				complete = false
			}
		}
		if complete {
			header, columns = i, mapping
			break
		}
	}
	if header < 0 {
		return nil, api(422, "question_table_headers_required")
	}
	rows := []map[string]any{}
	for _, row := range records[header+1:] {
		item := map[string]any{"row": row.number}
		cellErrors := map[string]string{}
		nonempty := false
		for key, col := range columns {
			cell := row.cells[col]
			text := strings.TrimSpace(cell.text)
			if text != "" {
				nonempty = true
			}
			if cell.kind == "date" && text != "" {
				stamp, e := time.Parse("2006-01-02 15:04:05.999999", text)
				if e != nil {
					cellErrors[key] = "Некорректная дата в ячейке"
				}
				if e == nil {
					text = stamp.Format("02.01.2006")
				}
			}
			if text != "" && cell.kind != "text" && cell.kind != "number" && cell.kind != "date" {
				cellErrors[key] = "В ячейке ошибка Excel или неподдерживаемый тип значения"
			}
			item[key] = text
		}
		if !nonempty {
			continue
		}
		if len(cellErrors) > 0 {
			item["cell_errors"] = cellErrors
		}
		rows = append(rows, item)
		if len(rows) > 200 {
			return nil, api(413, "question_table_question_limit")
		}
	}
	if len(rows) == 0 {
		return nil, api(422, "question_table_empty")
	}
	metadata, err := questionImportMetadata(fields)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(map[string]any{"importFormat": "question_table", "rows": rows, "metadata": metadata})
	if len(raw) > 900000 {
		return nil, api(413, "owner_input_size_limit")
	}
	return raw, err
}
