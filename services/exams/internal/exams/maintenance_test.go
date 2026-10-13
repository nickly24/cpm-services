package exams

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	amqp "github.com/rabbitmq/amqp091-go"
)

// This test is deliberately opt-in. Its SQL tables are temporary, connection
// local copies of the migrated cpm_dev schema. AMQP entities have unique names
// and no relationship to the application's exchanges or consumer queues.
//
//	CPM_EXAMS_OUTBOX_INTEGRATION=1 go test ./internal/exams \
//	  -run '^TestOutboxLocalIntegration$' -count=1 -v
func TestOutboxLocalIntegration(t *testing.T) {
	if os.Getenv("CPM_EXAMS_OUTBOX_INTEGRATION") != "1" {
		t.Skip("opt-in local MySQL/RabbitMQ integration")
	}
	values := maintenanceTestEnv(t)
	if values["MYSQL_DATABASE"] != "cpm_dev" || values["MYSQL_HOST"] != "127.0.0.1" || values["MYSQL_PORT"] != "13306" {
		t.Fatal("integration requires exact loopback cpm_dev database")
	}
	brokerURL, err := url.Parse(values["RABBITMQ_URL"])
	if err != nil || brokerURL.Scheme != "amqp" || brokerURL.Hostname() != "127.0.0.1" || brokerURL.Port() != "5672" || brokerURL.Path != "/cpm_dev" || brokerURL.RawQuery != "" || brokerURL.Fragment != "" {
		t.Fatal("integration requires exact loopback cpm_dev RabbitMQ vhost")
	}
	if values["MYSQL_USER"] == "" || values["MYSQL_PASSWORD"] == "" || brokerURL.User == nil {
		t.Fatal("local integration credentials missing")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cfg := mysql.NewConfig()
	cfg.User = values["MYSQL_USER"]
	cfg.Passwd = values["MYSQL_PASSWORD"]
	cfg.Net = "tcp"
	cfg.Addr = "127.0.0.1:13306"
	cfg.DBName = "cpm_dev"
	cfg.ParseTime = true
	cfg.Loc = time.UTC
	cfg.Timeout = 3 * time.Second
	cfg.ReadTimeout = 8 * time.Second
	cfg.WriteTimeout = 8 * time.Second
	cfg.Params = map[string]string{"time_zone": "'+00:00'"}
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal("open local integration database failed")
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	db.SetConnMaxIdleTime(0)
	if err = db.PingContext(ctx); err != nil {
		t.Fatal("local integration database unavailable")
	}
	// The names intentionally match the real outbox/source tables, so both
	// production helpers exercise their actual SQL against identical schemas.

	for _, table := range []string{"exams_outbox_v1", "exams_source_v1"} {
		var name, ddl string
		if db.QueryRowContext(ctx, "SHOW CREATE TABLE "+table).Scan(&name, &ddl) != nil || name != table || !strings.HasPrefix(ddl, "CREATE TABLE `"+table+"`") {
			t.Fatal("read migrated outbox/source schema failed")
		}
		ddl = strings.Replace(ddl, "CREATE TABLE ", "CREATE TEMPORARY TABLE ", 1)
		if _, err = db.ExecContext(ctx, ddl); err != nil {
			var sqlError *mysql.MySQLError
			if errors.As(err, &sqlError) {
				t.Fatalf("create isolated integration SQL table failed (MySQL code %d)", sqlError.Number)
			}
			t.Fatal("create isolated integration SQL table failed")
		}
	}
	if _, err = db.ExecContext(ctx, "CREATE TEMPORARY TABLE exams_outbox_integration_guard(token CHAR(36) NOT NULL)"); err != nil {
		t.Fatal("create isolated SQL guard failed")
	}

	guard := UUID()
	if _, err = db.ExecContext(ctx, "INSERT INTO exams_outbox_integration_guard(token) VALUES(?)", guard); err != nil {
		t.Fatal("create SQL session guard failed")
	}
	checkGuard := func() {
		t.Helper()
		var token string
		if db.QueryRowContext(ctx, "SELECT token FROM exams_outbox_integration_guard").Scan(&token) != nil || token != guard {
			t.Fatal("integration SQL connection changed; refusing live table access")
		}
	}
	checkGuard()
	org := int64(9_000_000_000_001)
	exam := &Exam{ID: 9_000_000_000_002, OrganizationID: org, Revision: 7, Divisions: map[int64]*Division{1: {ID: 1}, 2: {ID: 2}}}
	checkTxGuard := func(tx *sql.Tx) {
		t.Helper()
		var token string
		if tx.QueryRowContext(ctx, "SELECT token FROM exams_outbox_integration_guard").Scan(&token) != nil || token != guard {
			_ = tx.Rollback()
			t.Fatal("integration SQL transaction lacks temporary guard")
		}
	}
	begin := func() *sql.Tx {
		t.Helper()
		checkGuard()
		tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if err != nil {
			t.Fatal("begin isolated SQL transaction failed")
		}
		checkTxGuard(tx)
		return tx
	}
	sourceCounts := func() (int, int) {
		t.Helper()
		checkGuard()
		var sources, events int
		if db.QueryRowContext(ctx, "SELECT COUNT(*) FROM exams_source_v1 WHERE organization_id=?", org).Scan(&sources) != nil || db.QueryRowContext(ctx, "SELECT COUNT(*) FROM exams_outbox_v1 WHERE organization_id=?", org).Scan(&events) != nil {
			t.Fatal("read isolated source counts failed")
		}
		return sources, events
	}
	// Rollback must undo every division source change and every corresponding
	// event. Duplicate division IDs generate no duplicate source increments.
	tx := begin()
	if err = sourceChange(ctx, tx, exam, []int64{2, 1, 2}, "integration-rollback"); err != nil {
		_ = tx.Rollback()
		t.Fatal("transactional source change failed")
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal("source rollback failed")
	}
	if sources, events := sourceCounts(); sources != 0 || events != 0 {
		t.Fatalf("rollback persisted %d sources and %d events", sources, events)
	}
	tx = begin()
	if err = sourceChange(ctx, tx, exam, []int64{2, 1, 2}, "integration-commit"); err != nil {
		_ = tx.Rollback()
		t.Fatal("source commit preparation failed")
	}
	if err = tx.Commit(); err != nil {
		t.Fatal("source commit failed")
	}
	if sources, events := sourceCounts(); sources != 2 || events != 2 {
		t.Fatalf("commit created %d sources and %d events", sources, events)
	}
	var id string
	var envelope []byte
	checkGuard()
	if err = db.QueryRowContext(ctx, "SELECT id,envelope FROM exams_outbox_v1 WHERE organization_id=? AND division_id=1", org).Scan(&id, &envelope); err != nil {
		t.Fatal("committed outbox event missing")
	}
	var parsed struct {
		MessageID   string `json:"message_id"`
		MessageType string `json:"message_type"`
		Payload     struct {
			OrganizationID int64 `json:"organization_id"`
			DivisionID     int64 `json:"division_id"`
			SourceRevision int64 `json:"source_revision"`
			ExamRevision   int64 `json:"exam_revision"`
		} `json:"payload"`
	}
	if !validateKey(id) || json.Unmarshal(envelope, &parsed) != nil || parsed.MessageID != id || parsed.MessageType != "exams.source.changed" || parsed.Payload.OrganizationID != org || parsed.Payload.DivisionID != 1 || parsed.Payload.SourceRevision != 1 || parsed.Payload.ExamRevision != 7 {
		t.Fatal("incorrect transactional outbox envelope")
	}
	sourceVector := func() map[int64]int64 {
		t.Helper()
		checkGuard()
		rows, err := db.QueryContext(ctx, "SELECT division_id,revision FROM exams_source_v1 WHERE organization_id=? ORDER BY division_id", org)
		if err != nil {
			t.Fatal("read source revision vector failed")
		}
		defer rows.Close()
		vector := map[int64]int64{}
		for rows.Next() {
			var division, revision int64
			if rows.Scan(&division, &revision) != nil {
				t.Fatal("scan source revision vector failed")
			}
			vector[division] = revision
		}
		if rows.Err() != nil {
			t.Fatal("read source revision vector interrupted")
		}
		return vector
	}
	assertVector := func() {
		t.Helper()
		vector := sourceVector()
		if len(vector) != 2 || vector[1] != 1 || vector[2] != 1 {
			t.Fatal("delivery or rollback changed committed source revision vector")
		}
	}
	tx = begin()
	if err = sourceChange(ctx, tx, exam, []int64{1, 2}, "integration-second-rollback"); err != nil {
		_ = tx.Rollback()
		t.Fatal("prepare second source rollback failed")
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal("second source rollback failed")
	}
	assertVector()
	if _, count := sourceCounts(); count != 2 {
		t.Fatal("rollback appended extra events")
	}
	dial := func() (*amqp.Connection, error) {
		return amqp.DialConfig(values["RABBITMQ_URL"], amqp.Config{Heartbeat: 5 * time.Second, Dial: func(network, address string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, network, address)
		}})
	}
	conn, err := dial()
	if err != nil {
		t.Fatal("local integration RabbitMQ unavailable")
	}
	defer conn.Close()
	channel, err := conn.Channel()
	if err != nil {
		t.Fatal("local integration RabbitMQ channel unavailable")
	}
	defer channel.Close()
	suffix := UUID()
	exchange := "exams.integration." + suffix
	queueName := "exams.integration." + suffix
	if channel.ExchangeDeclare(exchange, "topic", true, false, false, false, nil) != nil {
		t.Fatal("declare isolated integration exchange failed")
	}
	queueDeclared := false
	// Cleanup names are local test constants; never delete application entities.
	defer func() {
		cleanupChannel := channel
		var cleanupConn *amqp.Connection
		if cleanupChannel.IsClosed() {
			cleanupConn, err = amqp.DialConfig(values["RABBITMQ_URL"], amqp.Config{Heartbeat: 5 * time.Second, Dial: func(network, address string) (net.Conn, error) {
				return (&net.Dialer{Timeout: 3 * time.Second}).Dial(network, address)
			}})
			if err != nil {
				t.Error("isolated RabbitMQ cleanup connection failed")
				return
			}
			defer cleanupConn.Close()
			cleanupChannel, err = cleanupConn.Channel()
			if err != nil {
				t.Error("isolated RabbitMQ cleanup channel failed")
				return
			}
			defer cleanupChannel.Close()
		}
		if queueDeclared {
			if _, cleanupErr := cleanupChannel.QueueDelete(queueName, false, false, false); cleanupErr != nil {
				t.Error("isolated integration queue cleanup failed")
			}
		}
		if cleanupErr := cleanupChannel.ExchangeDelete(exchange, false, false); cleanupErr != nil {
			t.Error("isolated integration exchange cleanup failed")
		}
	}()
	if channel.Confirm(false) != nil {
		t.Fatal("publisher confirm setup failed")
	}
	returned := channel.NotifyReturn(make(chan amqp.Return, 4))
	readStored := func() (bool, []byte) {
		t.Helper()
		checkGuard()
		var published sql.NullTime
		var raw []byte
		if db.QueryRowContext(ctx, "SELECT published_at,envelope FROM exams_outbox_v1 WHERE id=?", id).Scan(&published, &raw) != nil {
			t.Fatal("read isolated publication receipt failed")
		}
		if !bytes.Equal(envelope, raw) {
			t.Fatal("delivery mutated stored event envelope")
		}
		return published.Valid, raw
	}
	deliver := func() (bool, error) {
		t.Helper()
		checkGuard()
		return deliverOutboxTo(ctx, db, channel, returned, exchange, id)
	}
	// Mandatory unroutable publications must remain pending even though RabbitMQ
	// acknowledges receipt at the publisher-confirm level.
	for attempt := 0; attempt < 2; attempt++ {
		worked, err := deliver()
		if err != nil {
			t.Fatal("mandatory-unroutable delivery failed")
		}
		if worked {
			t.Fatal("unroutable event was marked delivered")
		}
		if published, _ := readStored(); published {
			t.Fatal("unroutable event acquired published_at")
		}
		assertVector()
	}
	queue, err := channel.QueueDeclare(queueName, true, false, false, false, amqp.Table{"x-expires": int32(120000)})
	if err != nil {
		t.Fatal("declare isolated durable integration queue failed")
	}
	queueDeclared = true
	if channel.QueueBind(queue.Name, "exams.source.changed", exchange, false, nil) != nil {
		t.Fatal("bind isolated integration queue failed")
	}
	consumeOne := func() {
		t.Helper()
		message, ok, err := channel.Get(queue.Name, false)
		if err != nil || !ok {
			t.Fatal("confirmed integration publication was not delivered")
		}
		if message.MessageId != id || message.DeliveryMode != amqp.Persistent || message.ContentType != "application/json" || !bytes.Equal(message.Body, envelope) {
			t.Fatal("delivery altered message ID or event payload")
		}
		if err = message.Ack(false); err != nil {
			t.Fatal("ack isolated integration message failed")
		}
	}
	worked, err := deliver()
	if err != nil || !worked {
		t.Fatal("bound publication was not confirmed")
	}
	if published, _ := readStored(); !published {
		t.Fatal("confirmed publication missing published_at")
	}
	consumeOne()
	assertVector()
	worked, err = deliver()
	if err != nil || worked {
		t.Fatal("already published event was sent again")
	}
	// The primary key must reject reusing a message ID without overwriting the
	// event or advancing either division's source revision.
	checkGuard()
	_, err = db.ExecContext(ctx, "INSERT INTO exams_outbox_v1(id,organization_id,division_id,source_revision,envelope) VALUES(?,?,?,?,?)", id, org, 1, 1, envelope)
	var duplicate *mysql.MySQLError
	if !errors.As(err, &duplicate) || duplicate.Number != 1062 {
		t.Fatal("duplicate message ID was not rejected")
	}
	assertVector()
	if published, _ := readStored(); !published {
		t.Fatal("duplicate insert reset publication receipt")
	}
	// Simulate the crash window after broker confirmation but before recording
	// published_at. Recovery may deliver a duplicate with the same message ID;
	// the payload and source revision vector must stay identical.
	checkGuard()
	if _, err = db.ExecContext(ctx, "UPDATE exams_outbox_v1 SET published_at=NULL WHERE id=?", id); err != nil {
		t.Fatal("prepare isolated crash-window recovery failed")
	}
	worked, err = deliver()
	if err != nil || !worked {
		t.Fatal("pending publication recovery failed")
	}
	consumeOne()
	assertVector()
	if published, _ := readStored(); !published {
		t.Fatal("recovered publication missing receipt")
	}
	var untouched sql.NullTime
	checkGuard()
	if db.QueryRowContext(ctx, "SELECT published_at FROM exams_outbox_v1 WHERE organization_id=? AND division_id=2", org).Scan(&untouched) != nil || untouched.Valid {
		t.Fatal("targeted publisher changed another outbox event")
	}
	if _, extra, consumeErr := channel.Get(queue.Name, true); consumeErr != nil || extra {
		t.Fatal("targeted publisher left unexpected integration messages")
	}
	t.Log("real MySQL/RabbitMQ: atomic rollback, unroutable retry, confirmed durable publication, duplicate ID and crash-window recovery passed")
}

func maintenanceTestEnv(t *testing.T) map[string]string {
	t.Helper()
	path := os.Getenv("CPM_EXAMS_TEST_ENV")
	if path == "" {
		path = filepath.Join("..", "..", "..", "..", "dev", "local", "exams.env")
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal("local exams integration configuration missing")
	}
	defer file.Close()
	allowed := map[string]bool{"MYSQL_HOST": true, "MYSQL_PORT": true, "MYSQL_DATABASE": true, "MYSQL_USER": true, "MYSQL_PASSWORD": true, "RABBITMQ_URL": true}
	values := map[string]string{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(strings.TrimPrefix(line, "export "), "=")
		key = strings.TrimSpace(key)
		if !ok || !allowed[key] {
			continue
		}
		value = strings.TrimSpace(value)
		if strings.HasPrefix(value, `"`) {
			decoded, err := strconv.Unquote(value)
			if err != nil {
				t.Fatal("invalid local integration configuration")
			}
			value = decoded
		} else if len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'' {
			value = value[1 : len(value)-1]
		}
		values[key] = value
	}
	if scanner.Err() != nil {
		t.Fatal("read local integration configuration failed")
	}
	for key := range allowed {
		if value, exists := os.LookupEnv(key); exists {
			values[key] = value
		}
	}
	return values
}
