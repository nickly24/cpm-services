CREATE TABLE IF NOT EXISTS exams_catalog_v1 (
 id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
 organization_id BIGINT NOT NULL,
 owner_division_id BIGINT NOT NULL,
 revision BIGINT NOT NULL,
 next_id BIGINT NOT NULL,
 document JSON NOT NULL,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
 KEY exams_organization(organization_id,id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE IF NOT EXISTS exams_receipts_v1 (
 organization_id BIGINT NOT NULL,
 account_id BIGINT NOT NULL,
 idempotency_key VARCHAR(36) NOT NULL,
 division_id BIGINT NOT NULL,
 request_hash CHAR(64) NOT NULL,
 response JSON NULL,
 status_code INT NOT NULL DEFAULT 200,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 PRIMARY KEY(organization_id,account_id,idempotency_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE IF NOT EXISTS exams_source_v1 (
 organization_id BIGINT NOT NULL,
 division_id BIGINT NOT NULL,
 revision BIGINT NOT NULL DEFAULT 0,
 PRIMARY KEY(organization_id,division_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE IF NOT EXISTS exams_outbox_v1 (
 id CHAR(36) PRIMARY KEY,
 organization_id BIGINT NOT NULL,
 division_id BIGINT NOT NULL,
 source_revision BIGINT NOT NULL,
 envelope JSON NOT NULL,
 published_at DATETIME(6) NULL,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 KEY exams_delivery(published_at,created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE IF NOT EXISTS exams_imports_v1 (
 id CHAR(36) PRIMARY KEY,
 organization_id BIGINT NOT NULL,
 division_id BIGINT NOT NULL,
 account_id BIGINT NOT NULL,
 exam_id BIGINT NOT NULL,
 kind VARCHAR(16) NOT NULL,
 revision BIGINT NOT NULL DEFAULT 1,
 state VARCHAR(16) NOT NULL DEFAULT 'editable',
 rows_json JSON NOT NULL,
 result JSON NULL,
 expires_at DATETIME(6) NOT NULL,
 KEY exams_import_expiry(expires_at),
 CONSTRAINT exams_import_exam FOREIGN KEY(exam_id) REFERENCES exams_catalog_v1(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE IF NOT EXISTS exams_training_progress_v1 (
 organization_id BIGINT NOT NULL,
 division_id BIGINT NOT NULL,
 account_id BIGINT NOT NULL,
 exam_id BIGINT NOT NULL,
 question_id BIGINT NOT NULL,
 fingerprint CHAR(64) NOT NULL,
 PRIMARY KEY(organization_id,division_id,account_id,exam_id,question_id),
 CONSTRAINT exams_progress_exam FOREIGN KEY(exam_id) REFERENCES exams_catalog_v1(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE IF NOT EXISTS exams_training_settings_v1 (
 organization_id BIGINT NOT NULL,
 division_id BIGINT NOT NULL,
 account_id BIGINT NOT NULL,
 exam_id BIGINT NOT NULL,
 part_id BIGINT NOT NULL DEFAULT 0,
 settings JSON NOT NULL,
 PRIMARY KEY(organization_id,division_id,account_id,exam_id,part_id),
 CONSTRAINT exams_settings_exam FOREIGN KEY(exam_id) REFERENCES exams_catalog_v1(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
