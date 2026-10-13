-- Execution uses RabbitMQ. These tables hold delivery proofs, not a SQL worker queue.
SET @uploads_generation_ddl = (SELECT IF(EXISTS (
 SELECT 1 FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE()
 AND TABLE_NAME='uploads_jobs_v1' AND COLUMN_NAME='delivery_generation'
), 'SELECT 1', 'ALTER TABLE uploads_jobs_v1 ADD COLUMN delivery_generation BIGINT NOT NULL DEFAULT 0'));
PREPARE uploads_generation_stmt FROM @uploads_generation_ddl;
EXECUTE uploads_generation_stmt;
DEALLOCATE PREPARE uploads_generation_stmt;

CREATE TABLE IF NOT EXISTS uploads_outbox_v1 (
 id CHAR(32) PRIMARY KEY, job_id CHAR(32) NOT NULL, generation BIGINT NOT NULL,
 organization_id BIGINT NOT NULL, division_id BIGINT NOT NULL, actor_account_id BIGINT NOT NULL,
 type VARCHAR(64) NOT NULL, owner VARCHAR(24) NOT NULL,
 route VARCHAR(64) NOT NULL, body JSON NOT NULL,
 published_at DATETIME(6) NULL, available_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 lease_token CHAR(32) NULL, lease_until DATETIME(6) NULL, attempts INT NOT NULL DEFAULT 0,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 INDEX uploads_outbox_dispatch(published_at,available_at,lease_until),
 INDEX uploads_outbox_job(job_id,generation,created_at),
 INDEX uploads_outbox_scope(organization_id,division_id,actor_account_id)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS uploads_inbox_v1 (
 id CHAR(32) PRIMARY KEY, job_id CHAR(32) NOT NULL, generation BIGINT NOT NULL,
 organization_id BIGINT NOT NULL, division_id BIGINT NOT NULL, actor_account_id BIGINT NOT NULL,
 type VARCHAR(64) NOT NULL, owner VARCHAR(24) NOT NULL, body_hash CHAR(64) NOT NULL,
 state VARCHAR(16) NOT NULL, lease_token CHAR(32) NULL, lease_until DATETIME(6) NULL,
 attempts INT NOT NULL DEFAULT 0, error_code VARCHAR(64) NOT NULL DEFAULT '',
 completed_at DATETIME(6) NULL, created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
 INDEX uploads_inbox_recovery(state,lease_until),
 INDEX uploads_inbox_job(job_id,generation),
 INDEX uploads_inbox_scope(organization_id,division_id,actor_account_id)
) ENGINE=InnoDB;
