CREATE TABLE IF NOT EXISTS ratings_scopes_v1 (
 organization_id BIGINT NOT NULL, division_id BIGINT NOT NULL,
 revision BIGINT NOT NULL DEFAULT 0, active_job_id CHAR(36) NULL,
 published_job_id CHAR(36) NULL, updated_at DATETIME(6) NOT NULL,
 PRIMARY KEY(organization_id,division_id)
);
CREATE TABLE IF NOT EXISTS ratings_jobs_v1 (
 id CHAR(36) PRIMARY KEY, organization_id BIGINT NOT NULL, division_id BIGINT NOT NULL,
 revision BIGINT NOT NULL, status VARCHAR(32) NOT NULL,
 date_from DATE NOT NULL, date_to DATE NOT NULL, as_of DATETIME(6) NOT NULL,
 created_by BIGINT NOT NULL, idempotency_key VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 request_hash CHAR(64) NOT NULL, snapshot JSON NULL, source_hash CHAR(64) NULL,
 total_count INT NOT NULL DEFAULT 0, processed_count INT NOT NULL DEFAULT 0,
 error_code VARCHAR(100) NOT NULL DEFAULT '', attempts INT NOT NULL DEFAULT 0,
 next_attempt_at DATETIME(6) NOT NULL, lease_token CHAR(36) NULL, lease_until DATETIME(6) NULL,
 created_at DATETIME(6) NOT NULL, completed_at DATETIME(6) NULL,
 UNIQUE KEY request_identity(organization_id,division_id,created_by,idempotency_key),
 UNIQUE KEY scope_revision(organization_id,division_id,revision),
 INDEX work(status,next_attempt_at,lease_until), INDEX scope_list(organization_id,division_id,created_at)
);
CREATE TABLE IF NOT EXISTS ratings_items_v1 (
 job_id CHAR(36) NOT NULL, account_id BIGINT NOT NULL, input_hash CHAR(64) NOT NULL,
 input JSON NOT NULL, profile JSON NOT NULL, status VARCHAR(16) NOT NULL DEFAULT 'queued',
 output JSON NULL, output_hash CHAR(64) NULL, error_code VARCHAR(100) NOT NULL DEFAULT '',
 message_id CHAR(36) NOT NULL, created_at DATETIME(6) NOT NULL, completed_at DATETIME(6) NULL,
 PRIMARY KEY(job_id,account_id), UNIQUE KEY message_identity(message_id),
 FOREIGN KEY(job_id) REFERENCES ratings_jobs_v1(id)
);
CREATE TABLE IF NOT EXISTS ratings_outbox_v1 (
 message_id CHAR(36) PRIMARY KEY, job_id CHAR(36) NOT NULL,
 body JSON NOT NULL, attempts INT NOT NULL DEFAULT 0, next_attempt_at DATETIME(6) NOT NULL,
 lease_token CHAR(36) NULL, lease_until DATETIME(6) NULL, delivered_at DATETIME(6) NULL,
 created_at DATETIME(6) NOT NULL, INDEX delivery(delivered_at,next_attempt_at,lease_until),
 FOREIGN KEY(job_id) REFERENCES ratings_jobs_v1(id)
);
