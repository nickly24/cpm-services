package uploads

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestQuestionTableSixThousandPhysicalRowsAndLargeOwnerEnvelope(t *testing.T) {
	var sheet strings.Builder
	sheet.WriteString(`<row r="1">`)
	for i, label := range []string{"Название теста", "Номер", "Категория вопроса", "Вопрос", "Варианты ответов", "Правильный ответ"} {
		sheet.WriteString(sourceTestCell(fmt.Sprintf("%c1", 'A'+i), label))
	}
	sheet.WriteString(`</row>`)
	for i := 2; i <= 6001; i++ {
		fmt.Fprintf(&sheet, `<row r="%d">`, i)
		for _, cell := range []struct{ col, value string }{{"A", "Большой тест"}, {"B", fmt.Sprint(i - 1)}, {"D", strings.Repeat("Вопрос ", 35)}, {"F", "первый\nвторой / третий"}} {
			sheet.WriteString(sourceTestCell(fmt.Sprintf("%s%d", cell.col, i), cell.value))
		}
		sheet.WriteString(`</row>`)
	}
	file := sourceTestArchive(t, sheet.String(), nil)
	raw, err := questionTableInput(file, map[string]string{"timeLimitMinutes": "180"})
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Rows []json.RawMessage `json:"rows"`
	}
	if json.Unmarshal(raw, &parsed) != nil || len(parsed.Rows) != 6000 || len(raw) <= 1<<20 {
		t.Fatal("large input lost rows or failed to cross old size limit")
	}
	input, _ := json.Marshal(stagedJSONInput{DirectionID: 19, Payload: raw})
	ss := Session{ID: strings.Repeat("a", 32), Type: "tests.json", Scope: Scope{OrganizationID: 42, DivisionID: 7, ActorID: 9}}
	if _, err = stagedEnvelope(ss, input, 0); err != nil {
		t.Fatal(err)
	}
	// Zero means no business row-count bound only for the question table; other imports retain theirs.
	if err = validFile(file, "xlsx", 100, true); err == nil {
		t.Fatal("unrelated import row guard was disabled")
	}
}
