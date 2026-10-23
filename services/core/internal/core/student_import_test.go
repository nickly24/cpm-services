package core

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"github.com/xuri/excelize/v2"
	"testing"
)

func TestStudentWorkbookExportUsesFullStreamingDimensions(t *testing.T) {
	book := excelize.NewFile()
	book.SetCellStr("Sheet1", "A1", "ФИО")
	book.SetCellStr("Sheet1", "G3", "Создан")
	value, status, e := workbookBase64(book)
	if e != nil || status != 200 {
		t.Fatal("workbook serialization failed", e)
	}
	raw, e := base64.StdEncoding.DecodeString(value.(map[string]any)["content_base64"].(string))
	if e != nil {
		t.Fatal(e)
	}
	read, e := excelize.OpenReader(bytes.NewReader(raw))
	if e != nil {
		t.Fatal(e)
	}
	defer read.Close()
	dimension, e := read.GetSheetDimension("Sheet1")
	if e != nil || dimension != "A1:G3" {
		t.Fatal("streaming readers would lose exported cells", dimension, e)
	}
}

func studentTestWorkbook(t *testing.T, rows int, formula bool) []byte {
	t.Helper()
	b := excelize.NewFile()
	defer b.Close()
	for i, h := range studentHeaders {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		b.SetCellStr("Sheet1", cell, h)
	}
	for i := 0; i < rows; i++ {
		cell, _ := excelize.CoordinatesToCellName(1, i+2)
		b.SetCellStr("Sheet1", cell, "QA Ученик")
		if formula {
			b.SetCellFormula("Sheet1", cell, "1+1")
		}
	}
	v, e := b.WriteToBuffer()
	if e != nil {
		t.Fatal(e)
	}
	return v.Bytes()
}
func TestStudentImportWorkbookNoProductHundredCap(t *testing.T) {
	for _, n := range []int{1, 101, 333, 1000} {
		rows, e := parseStudentWorkbook(studentTestWorkbook(t, n, false))
		if e != nil || len(rows) != n {
			t.Fatalf("rows=%d err=%v", n, e)
		}
		if rows[0].Class != "" || rows[0].ProfileID != "" || rows[0].School != "" {
			t.Fatal("optional values were invented")
		}
	}
	if _, e := parseStudentWorkbook(studentTestWorkbook(t, 1, true)); e == nil {
		t.Fatal("formula accepted")
	}
}
func TestStudentOptionalClassWirePreservesStaffAndNull(t *testing.T) {
	for _, raw := range []string{`{"role":"student"}`, `{"role":"student","class":null}`, `{"role":"student","class":0}`, `{"role":"student","class":7}`} {
		var u UserDTO
		if json.Unmarshal([]byte(raw), &u) != nil {
			t.Fatal("decode")
		}
		if raw == `{"role":"student","class":0}` && !u.classSupplied {
			t.Fatal("explicit zero lost")
		}
	}
	b, _ := json.Marshal(UserDTO{Role: "student"})
	if !bytes.Contains(b, []byte(`"class":null`)) {
		t.Fatal("missing class not null")
	}
	b, _ = json.Marshal(UserDTO{Role: "proctor"})
	if !bytes.Contains(b, []byte(`"class":0`)) {
		t.Fatal("old staff proofs changed")
	}
}
func TestStudentCredentialVaultBindsActorScopeRevision(t *testing.T) {
	s := &Server{InternalKey: "abcdefghijklmnopqrstuvwxyz0123456789"}
	d := studentDraft{ID: "qa", Org: 1, Division: 2, Actor: 3, Revision: 1, Payload: studentDraftPayload{Rows: []StudentImportRow{{Password: "synthetic-secret"}}}}
	blob, e := s.studentSeal(d)
	if e != nil || bytes.Contains(blob, []byte("synthetic-secret")) {
		t.Fatal("not encrypted")
	}
	c, _ := s.studentCipher()
	if _, e = c.Open(nil, blob[:c.NonceSize()], blob[c.NonceSize():], studentAAD(d)); e != nil {
		t.Fatal(e)
	}
	d.Actor++
	if _, e = c.Open(nil, blob[:c.NonceSize()], blob[c.NonceSize():], studentAAD(d)); e == nil {
		t.Fatal("foreign actor opened vault")
	}
}
