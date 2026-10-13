-- New homework-owned tables only. No legacy DDL or production migration.
-- External organization/division/account identities are validated through core/auth,
-- rather than foreign keys to another service's tables.
CREATE TABLE IF NOT EXISTS hw_types (
 id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
 organization_id BIGINT NOT NULL,
 division_id BIGINT NOT NULL,
 name VARCHAR(100) NOT NULL,
 archived BOOLEAN NOT NULL DEFAULT FALSE,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
 UNIQUE KEY uq_hw_type_name (organization_id,division_id,name),
 UNIQUE KEY uq_hw_type_scope (organization_id,division_id,id),
 CHECK (organization_id > 0 AND division_id > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS hw_assignments (
 id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
 organization_id BIGINT NOT NULL,
 division_id BIGINT NOT NULL,
 type_id BIGINT NOT NULL,
 name VARCHAR(200) NOT NULL,
 deadline DATE NOT NULL,
 published BOOLEAN NOT NULL DEFAULT FALSE,
 deleted_at DATETIME(6) NULL,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
 UNIQUE KEY uq_hw_assignment_scope (organization_id,division_id,id),
 KEY ix_hw_assignment_catalog (organization_id,division_id,deleted_at,published,deadline,id),
 CONSTRAINT fk_hw_assignment_type FOREIGN KEY (organization_id,division_id,type_id)
 REFERENCES hw_types(organization_id,division_id,id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS hw_results (
 id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
 assignment_id BIGINT NOT NULL,
 account_id BIGINT NOT NULL,
 result INT NULL,
 status BOOLEAN NOT NULL DEFAULT FALSE,
 date_pass DATE NULL,
 manual BOOLEAN NOT NULL DEFAULT TRUE,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
 UNIQUE KEY uq_hw_result_student (assignment_id,account_id),
 KEY ix_hw_result_student (account_id,assignment_id),
 CONSTRAINT fk_hw_result_assignment FOREIGN KEY (assignment_id) REFERENCES hw_assignments(id),
 CHECK (account_id > 0),
 CHECK (result IS NULL OR (result >= 0 AND result <= 100))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS hw_submissions (
 id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
 assignment_id BIGINT NOT NULL,
 account_id BIGINT NOT NULL,
 state VARCHAR(32) NOT NULL DEFAULT 'none',
 draft_file_id BIGINT NULL,
 current_file_id BIGINT NULL,
 reviewer_account_id BIGINT NULL,
 submitted_at_utc DATETIME(6) NULL,
 generation INT NOT NULL DEFAULT 0,
 revision_count INT NOT NULL DEFAULT 0,
 revision_comment TEXT NOT NULL,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
 UNIQUE KEY uq_hw_submission_student (assignment_id,account_id),
 KEY ix_hw_submission_review (state,reviewer_account_id,submitted_at_utc,id),
 CONSTRAINT fk_hw_submission_assignment FOREIGN KEY (assignment_id) REFERENCES hw_assignments(id),
 CHECK (account_id > 0),
 CHECK (generation >= 0 AND revision_count >= 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS hw_files (
 id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
 submission_id BIGINT NOT NULL,
 object_key VARCHAR(700) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 status VARCHAR(32) NOT NULL,
 size_bytes BIGINT NOT NULL,
 page_count INT NOT NULL,
 sha256 CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 etag VARCHAR(200) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 UNIQUE KEY uq_hw_file_object (object_key),
 KEY ix_hw_file_submission (submission_id,id),
 CONSTRAINT fk_hw_file_submission FOREIGN KEY (submission_id) REFERENCES hw_submissions(id),
 CHECK (size_bytes >= 0 AND page_count >= 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS hw_jobs (
 id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
 client_upload_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 submission_id BIGINT NOT NULL,
 assignment_id BIGINT NOT NULL,
 account_id BIGINT NOT NULL,
 organization_id BIGINT NOT NULL,
 division_id BIGINT NOT NULL,
 generation INT NOT NULL,
 status VARCHAR(32) NOT NULL,
 stage VARCHAR(32) NOT NULL DEFAULT 'upload',
 progress INT NOT NULL DEFAULT 0,
 source_key VARCHAR(700) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 source_etag VARCHAR(200) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
 source_size BIGINT NOT NULL DEFAULT 0,
 output_key VARCHAR(700) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 settings_json JSON NOT NULL,
 attempts INT NOT NULL DEFAULT 0,
 manual_attempts INT NOT NULL DEFAULT 0,
 error_code VARCHAR(100) NULL,
 available_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 lease_until DATETIME(6) NULL,
 lease_token CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NULL,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
 UNIQUE KEY uq_hw_job_upload (account_id,client_upload_id),
 UNIQUE KEY uq_hw_job_source (source_key),
 UNIQUE KEY uq_hw_job_output (output_key),
 KEY ix_hw_job_dispatch (status,available_at,lease_until,id),
 KEY ix_hw_job_submission (submission_id,created_at,id),
 CONSTRAINT fk_hw_job_submission FOREIGN KEY (submission_id) REFERENCES hw_submissions(id),
 CONSTRAINT fk_hw_job_assignment FOREIGN KEY (assignment_id) REFERENCES hw_assignments(id),
 CHECK (account_id > 0 AND organization_id > 0 AND division_id > 0),
 CHECK (generation >= 0 AND attempts >= 0 AND manual_attempts >= 0),
 CHECK (progress >= 0 AND progress <= 100 AND source_size >= 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS hw_outbox (
 id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
 event_type VARCHAR(100) NOT NULL,
 body_json JSON NOT NULL,
 published_at DATETIME(6) NULL,
 available_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 lease_until DATETIME(6) NULL,
 attempts INT NOT NULL DEFAULT 0,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 KEY ix_hw_outbox_pending (published_at,available_at,lease_until,created_at),
 CHECK (attempts >= 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS hw_delete_jobs (
 id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
 object_key VARCHAR(700) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 status VARCHAR(32) NOT NULL DEFAULT 'pending',
 attempts INT NOT NULL DEFAULT 0,
 available_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 lease_until DATETIME(6) NULL,
 error_code VARCHAR(100) NULL,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
 UNIQUE KEY uq_hw_delete_object (object_key),
 KEY ix_hw_delete_pending (status,available_at,lease_until,id),
 CHECK (attempts >= 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS hw_audit (
 id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
 actor_id BIGINT NULL,
 organization_id BIGINT NOT NULL,
 division_id BIGINT NOT NULL,
 action VARCHAR(100) NOT NULL,
 entity_id VARCHAR(100) NOT NULL,
 detail_json JSON NULL,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 KEY ix_hw_audit_scope (organization_id,division_id,id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
