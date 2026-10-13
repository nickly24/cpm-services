package exams

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestPinnedImportRequiresWholeCurrentSnapshot(t *testing.T) {
	rows := make([]ImportRow, 5000)
	for i := range rows {
		rows[i] = ImportRow{RowID: fmt.Sprintf("row-%d", i+2), SourceRow: i + 2, Input: json.RawMessage(`{"studentId":9007199254740993,"grade":5}`), Errors: []string{}}
	}
	proof, err := importValidationHash(rows)
	if err != nil {
		t.Fatal(err)
	}
	source := strings.Repeat("a", 64)
	if err = verifyPinnedImport(3, source, 3, source, proof, rows); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*ImportRow){
		func(r *ImportRow) { r.Errors = []string{"student_not_found"} },
		func(r *ImportRow) { r.Excluded = true },
		func(r *ImportRow) { r.Input = json.RawMessage(`{"studentId":9007199254740992,"grade":5}`) },
	} {
		last := rows[4999]
		mutate(&rows[4999])
		auxError(t, verifyPinnedImport(3, source, 3, source, proof, rows), "import_validation_modified")
		rows[4999] = last
	}
	auxError(t, verifyPinnedImport(4, source, 3, source, proof, rows), "preview_modified")
	auxError(t, verifyPinnedImport(3, strings.Repeat("b", 64), 3, source, proof, rows), "import_source_modified")
	auxError(t, verifyPinnedImport(3, "", 3, source, proof, rows), "import_source_modified")
	for _, invalid := range []string{"", strings.Repeat("A", 64), strings.Repeat("z", 64), strings.Repeat("a", 63)} {
		auxError(t, verifyPinnedImport(3, source, 3, invalid, proof, rows), "operation_request_mismatch")
		auxError(t, verifyPinnedImport(3, source, 3, source, invalid, rows), "operation_request_mismatch")
	}
	auxError(t, verifyPinnedImport(3, source, 0, source, proof, rows), "operation_request_mismatch")
}
