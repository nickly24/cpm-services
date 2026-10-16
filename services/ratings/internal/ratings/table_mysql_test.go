package ratings

import (
	"database/sql"
	"encoding/json"
	"net"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	mysql "github.com/go-sql-driver/mysql"
)

// Optional real-MySQL regression. Temporary tables shadow service tables on
// this one local connection; no permanent or production rows are modified.
func TestTableMySQLPaginationAndLiteralUnicodeSearch(t *testing.T) {
	dsn := os.Getenv("CPM_RATINGS_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("dedicated local MySQL not supplied")
	}
	config, e := mysql.ParseDSN(dsn)
	if e != nil {
		t.Fatal(e)
	}
	host, _, e := net.SplitHostPort(config.Addr)
	if e != nil || config.Net != "tcp" || host != "127.0.0.1" || config.DBName != "cpm_dev" {
		t.Fatal("only loopback cpm_dev is allowed")
	}
	db, e := sql.Open("mysql", dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	for _, schema := range []string{
		`CREATE TEMPORARY TABLE ratings_scopes_v1(organization_id BIGINT,division_id BIGINT,published_job_id CHAR(36))`,
		`CREATE TEMPORARY TABLE ratings_jobs_v1(id CHAR(36),organization_id BIGINT,division_id BIGINT,revision BIGINT,status VARCHAR(32),date_from DATE,date_to DATE,as_of DATETIME(6),created_by BIGINT,total_count INT,processed_count INT,error_code VARCHAR(100),attempts INT,created_at DATETIME(6),completed_at DATETIME(6),snapshot JSON,source_hash CHAR(64),lease_token CHAR(36))`,
		`CREATE TEMPORARY TABLE ratings_items_v1(job_id CHAR(36),account_id BIGINT,status VARCHAR(16),profile JSON,output JSON,PRIMARY KEY(job_id,account_id))`,
	} {
		if _, e = db.Exec(schema); e != nil {
			t.Fatal(e)
		}
	}
	j := fixtureJob()
	if _, e = db.Exec(`INSERT INTO ratings_jobs_v1 VALUES(?,?,?,?, 'completed','2026-10-01','2026-10-16',NOW(6),99,6,6,'',0,NOW(6),NOW(6),NULL,NULL,NULL)`, j.ID, j.OrganizationID, j.DivisionID, j.Revision); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(`INSERT INTO ratings_scopes_v1 VALUES(?,?,?)`, j.OrganizationID, j.DivisionID, j.ID); e != nil {
		t.Fatal(e)
	}
	var expected []tableRow
	for i, name := range []string{"Яков", "Ёлкин", "Елена", "Алексей", "Одинаковый", "Одинаковый"} {
		score := float64(20 - i/4)
		id := int64(i + 1)
		zero := 0.0
		row := tableRow{Account: Account{AccountID: id, FullName: name}, Result: tableResult{JobID: j.ID, Revision: j.Revision, OrganizationID: j.OrganizationID, DivisionID: j.DivisionID, AccountID: id, Algorithm: Algorithm, Status: "ready", InputHash: strings.Repeat("a", 64), Homework: tableComponent{&zero}, Exams: tableComponent{&zero}, Tests: tableComponent{&zero}, Final: &score}}
		p, _ := json.Marshal(row.Account)
		o, _ := json.Marshal(row.Result)
		if _, e = db.Exec(`INSERT INTO ratings_items_v1 VALUES(?,?,'ready',?,?)`, j.ID, id, p, o); e != nil {
			t.Fatal(e)
		}
		expected = append(expected, row)
	}
	sort.Slice(expected, func(i, k int) bool {
		a, b := expected[i], expected[k]
		if *a.Result.Final != *b.Result.Final {
			return *a.Result.Final > *b.Result.Final
		}
		if a.Account.FullName != b.Account.FullName {
			return a.Account.FullName < b.Account.FullName
		}
		return a.Account.AccountID < b.Account.AccountID
	})
	for i := range expected {
		expected[i].Place = i + 1
	}
	srv := &Server{Store: &Store{DB: db}}
	actor := Actor{Role: "admin", OrganizationAdmin: true, OrganizationID: j.OrganizationID}
	read := func(query string, offset int) ([]tableRow, int) {
		t.Helper()
		params := url.Values{"q": {query}, "version": {j.ID}, "limit": {"2"}, "offset": {strconv.Itoa(offset)}}
		w := httptest.NewRecorder()
		srv.table(w, httptest.NewRequest("GET", "/table?"+params.Encode(), nil), actor, j.DivisionID)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var env struct {
			Data struct {
				Ratings []tableRow `json:"ratings"`
				Total   int        `json:"total"`
			} `json:"data"`
		}
		if e = json.Unmarshal(w.Body.Bytes(), &env); e != nil {
			t.Fatal(e)
		}
		return env.Data.Ratings, env.Data.Total
	}
	for _, query := range []string{"", "е", "ё", "ел", "ёл", "%", "_", "null", "5"} {
		var want []tableRow
		for _, row := range expected {
			if query == "" || strings.Contains(strings.ToLower(row.Account.FullName), query) || strings.Contains(strconv.Itoa(int(row.Account.AccountID)), query) {
				want = append(want, row)
			}
		}
		for offset := 0; offset <= len(want)+1; offset += 2 {
			got, total := read(query, offset)
			if total != len(want) {
				t.Fatalf("query %q total=%d want=%d", query, total, len(want))
			}
			end := min(offset+2, len(want))
			start := min(offset, len(want))
			if len(got) != end-start {
				t.Fatal("wrong page size")
			}
			for i, row := range got {
				if row.Account.AccountID != want[start+i].Account.AccountID || row.Place != want[start+i].Place || *row.Result.Final != *want[start+i].Result.Final {
					t.Fatal("rank, tie order or summary changed")
				}
			}
		}
	}
}
