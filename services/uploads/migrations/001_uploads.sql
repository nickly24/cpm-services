CREATE TABLE IF NOT EXISTS uploads_sessions_v1 (
 id CHAR(32) PRIMARY KEY, organization_id BIGINT NOT NULL, division_id BIGINT NOT NULL,
 actor_account_id BIGINT NOT NULL, type VARCHAR(64) NOT NULL, owner VARCHAR(24) NOT NULL,
 owner_session_id VARCHAR(128) NOT NULL DEFAULT '', target_id VARCHAR(128) NOT NULL DEFAULT '',
 revision BIGINT NOT NULL DEFAULT 1, owner_revision BIGINT NOT NULL DEFAULT 1,
 state VARCHAR(32) NOT NULL, file_id CHAR(32) NOT NULL DEFAULT '', source_hash CHAR(64) NOT NULL,
 expires_at DATETIME(6) NOT NULL, created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 INDEX uploads_sessions_scope(organization_id,division_id,actor_account_id,created_at)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS uploads_files_v1 (
 id CHAR(32) PRIMARY KEY, organization_id BIGINT NOT NULL, division_id BIGINT NOT NULL,
 actor_account_id BIGINT NOT NULL, type VARCHAR(64) NOT NULL, owner VARCHAR(24) NOT NULL,
 owner_reference VARCHAR(128) NOT NULL DEFAULT '', target_id VARCHAR(128) NOT NULL DEFAULT '',
 source_hash CHAR(64) NOT NULL DEFAULT '', size_bytes BIGINT NOT NULL DEFAULT 0,
 extension VARCHAR(10) NOT NULL DEFAULT '', content MEDIUMBLOB NULL,
 expires_at DATETIME(6) NOT NULL, created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 INDEX uploads_files_scope(organization_id,division_id,actor_account_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS uploads_jobs_v1 (
 id CHAR(32) PRIMARY KEY, organization_id BIGINT NOT NULL, division_id BIGINT NOT NULL,
 actor_account_id BIGINT NOT NULL, session_id CHAR(32) NOT NULL UNIQUE,
 type VARCHAR(64) NOT NULL, owner VARCHAR(24) NOT NULL, status VARCHAR(32) NOT NULL,
 attempt INT NOT NULL DEFAULT 0, processed INT NOT NULL DEFAULT 0, total INT NOT NULL DEFAULT 1,
 error_code VARCHAR(64) NOT NULL DEFAULT '', report JSON NULL, lease_token CHAR(32) NULL,
 lease_until DATETIME(6) NULL, available_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6), updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
 INDEX uploads_jobs_poll(status,available_at,lease_until), INDEX uploads_jobs_scope(organization_id,division_id,actor_account_id,created_at)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS uploads_receipts_v1 (
 organization_id BIGINT NOT NULL, division_id BIGINT NOT NULL, actor_account_id BIGINT NOT NULL,
 command_id VARCHAR(128) NOT NULL, action VARCHAR(180) NOT NULL, request_hash CHAR(64) NOT NULL,
 resource_id CHAR(32) NOT NULL, state VARCHAR(32) NOT NULL,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 PRIMARY KEY(organization_id,division_id,actor_account_id,command_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS uploads_events_v1 (
 id BIGINT AUTO_INCREMENT PRIMARY KEY, organization_id BIGINT NOT NULL, division_id BIGINT NOT NULL,
 actor_account_id BIGINT NOT NULL, type VARCHAR(64) NOT NULL, owner VARCHAR(24) NOT NULL,
 resource_id CHAR(32) NOT NULL, action VARCHAR(64) NOT NULL, revision BIGINT NOT NULL DEFAULT 1,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 INDEX uploads_events_scope(organization_id,division_id,actor_account_id,id)
) ENGINE=InnoDB;
