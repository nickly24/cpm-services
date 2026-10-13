package attendance

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"
)

type attachmentDriver struct{}
type attachmentConn struct{}
type attachmentRows struct{ values []driver.Value }

func (attachmentDriver) Open(string) (driver.Conn, error)  { return attachmentConn{}, nil }
func (attachmentConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unsupported") }
func (attachmentConn) Close() error                        { return nil }
func (attachmentConn) Begin() (driver.Tx, error)           { return nil, errors.New("unsupported") }
func (attachmentConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if !strings.Contains(query, "WHERE organization_id=? AND zap_id=? AND id=?") || len(args) != 3 {
		return nil, errors.New("scope missing")
	}
	if args[0].Value != int64(1001) || args[1].Value != int64(5) {
		return &attachmentRows{}, nil
	}
	data := []byte("%PDF-1.7\nlegacy\x00bytes")
	switch args[2].Value {
	case int64(77):
		return &attachmentRows{[]driver.Value{"application/pdf", data, hash(data)}}, nil
	case int64(78):
		return &attachmentRows{[]driver.Value{"application/pdf", data, strings.Repeat("0", 64)}}, nil
	}
	return &attachmentRows{}, nil
}
func (*attachmentRows) Columns() []string { return []string{"mime", "content", "sha256"} }
func (*attachmentRows) Close() error      { return nil }
func (r *attachmentRows) Next(dest []driver.Value) error {
	if r.values == nil {
		return io.EOF
	}
	copy(dest, r.values)
	r.values = nil
	return nil
}

func init() { sql.Register("attendance-legacy-attachment-test", attachmentDriver{}) }

func TestLegacyAttachmentPreservesPDFAndRejectsForeignScopeAndCorruption(t *testing.T) {
	db, err := sql.Open("attendance-legacy-attachment-test", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := Store{DB: db}
	mime, data, err := s.ReadAttachment(context.Background(), 1001, 5, 77)
	if err != nil || mime != "application/pdf" || string(data) != "%PDF-1.7\nlegacy\x00bytes" {
		t.Fatal("original legacy bytes and MIME must be retained")
	}
	for _, scope := range [][3]int64{{1002, 5, 77}, {1001, 6, 77}, {1001, 5, 79}} {
		if _, _, err := s.ReadAttachment(context.Background(), scope[0], scope[1], scope[2]); err != missing {
			t.Fatal("foreign or missing attachment must not be returned")
		}
	}
	if _, _, err := s.ReadAttachment(context.Background(), 1001, 5, 78); err != unavailable {
		t.Fatal("corrupted bytes must not be served")
	}
}

func TestClientCannotSupplyStoredLegacyAttachmentReference(t *testing.T) {
	if checkImage(Image{MIME: "image/jpeg", AttachmentID: 77}) == nil {
		t.Fatal("stored reference may only be assigned by migration")
	}
}
