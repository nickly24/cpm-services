CREATE TABLE IF NOT EXISTS attendance_state_v1 (
 organization_id BIGINT NOT NULL PRIMARY KEY,
 revision BIGINT NOT NULL DEFAULT 0,
 document JSON NOT NULL,
 updated_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS attendance_audit_v1 (
 organization_id BIGINT NOT NULL,
 revision BIGINT NOT NULL,
 actor_account_id BIGINT NOT NULL,
 action VARCHAR(200) NOT NULL,
 snapshot JSON NOT NULL,
 created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 PRIMARY KEY(organization_id,revision)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS attendance_receipts_v1 (
 organization_id BIGINT NOT NULL,
 actor_account_id BIGINT NOT NULL,
 request_key VARCHAR(100) COLLATE utf8mb4_bin NOT NULL,
 request_hash CHAR(64) NOT NULL,
 response JSON NOT NULL,
 PRIMARY KEY(organization_id,actor_account_id,request_key)
) ENGINE=InnoDB;
