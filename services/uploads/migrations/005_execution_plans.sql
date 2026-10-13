-- Private immutable native bytes and authorization proof, never public job data.
-- Creating this ledger does not enable execution or authorize historical jobs.
CREATE TABLE IF NOT EXISTS uploads_execution_plans_v1 (
 job_id CHAR(32) PRIMARY KEY,
 organization_id BIGINT NOT NULL, division_id BIGINT NOT NULL, actor_account_id BIGINT NOT NULL,
 session_id CHAR(32) NOT NULL UNIQUE, type VARCHAR(64) NOT NULL, owner VARCHAR(24) NOT NULL,
 coordinator_command_key VARCHAR(128) NOT NULL, native_command_key CHAR(36) NOT NULL,
 issuance_key CHAR(36) NOT NULL, apply_method VARCHAR(8) NOT NULL, apply_path VARCHAR(255) NOT NULL,
 apply_body MEDIUMBLOB NOT NULL, confirmed_preview MEDIUMBLOB NULL,
 apply_body_sha256 CHAR(64) NOT NULL, preview_body MEDIUMBLOB NULL,
 preview_body_sha256 CHAR(64) NULL, binding_body MEDIUMBLOB NULL,
 original_grant_id CHAR(32) NULL, grant_id CHAR(32) NULL, grant_mode VARCHAR(16) NOT NULL DEFAULT 'apply',
 grant_expires_at DATETIME(6) NULL, apply_started_at DATETIME(6) NULL,
 owner_result MEDIUMBLOB NULL,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
 INDEX uploads_execution_scope(organization_id,division_id,actor_account_id,job_id)
) ENGINE=InnoDB;
