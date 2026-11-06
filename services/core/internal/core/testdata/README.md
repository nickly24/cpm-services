The examiner workflow MySQL test embeds `exam_workflow_mysql.sql` and creates
only connection-local temporary tables. It needs an empty dedicated MySQL 8
database whose name starts with `exams_upgrade_`, accessed over loopback TCP.
It does not require the application migrations or a production dump. Existing
tables in that database are shadowed for this connection and remain untouched.
All fixture tables and synthetic rows disappear when the connection closes.

From `services/core`, run:

```sh
CPM_EXAM_CORE_MYSQL_DSN_FILE=/absolute/path/to/local-test-dsn.txt go test ./internal/core -run TestExamWorkflowMySQL -count=1 -v
```

The DSN file is local configuration and must not be committed. Without this
environment variable, the integration test is skipped; ordinary unit tests
remain self-contained. The fixture retains relevant production column types,
primary and unique keys. MySQL does not support foreign keys on temporary
tables, so scope/active membership checks are exercised through the actual
Core code, with transaction rollback and paging executed by MySQL.
