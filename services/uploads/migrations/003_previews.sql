CREATE TABLE IF NOT EXISTS uploads_previews_v1 (
 session_id CHAR(32) PRIMARY KEY,
 organization_id BIGINT NOT NULL, division_id BIGINT NOT NULL,
 actor_account_id BIGINT NOT NULL, type VARCHAR(64) NOT NULL,
 revision BIGINT NOT NULL DEFAULT 1, target_revision BIGINT NOT NULL DEFAULT 0,
 source_hash CHAR(64) NOT NULL, input_hash CHAR(64) NOT NULL,
 preview_hash CHAR(64) NOT NULL, owner_preview_hash CHAR(64) NOT NULL,
 input MEDIUMBLOB NOT NULL, preview MEDIUMBLOB NOT NULL,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
 INDEX uploads_previews_scope(organization_id,division_id,actor_account_id)
) ENGINE=InnoDB;
