-- Relax nullability without rewriting values/IDs. No downgrade is required on code rollback.
SET SESSION lock_wait_timeout=5;
ALTER TABLE students MODIFY COLUMN `class` INT NULL DEFAULT NULL, ALGORITHM=INPLACE, LOCK=NONE;
-- New tables only. Existing profiles, QR IDs and credentials are not rewritten.
CREATE TABLE IF NOT EXISTS core_student_import_drafts (
 id CHAR(32) NOT NULL PRIMARY KEY,
 organization_id BIGINT NOT NULL,
 division_id BIGINT NOT NULL,
 actor_id INT NOT NULL,
 command_key VARCHAR(128) NOT NULL,
 source_sha256 CHAR(64) NOT NULL,
 revision BIGINT NOT NULL DEFAULT 1,
 state VARCHAR(16) NOT NULL DEFAULT 'preview',
 encrypted_payload MEDIUMBLOB NULL,
 expires_at DATETIME(6) NOT NULL,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 UNIQUE(organization_id,division_id,actor_id,command_key),
 INDEX(expires_at),
 FOREIGN KEY(organization_id,division_id) REFERENCES divisions(organization_id,id),
 FOREIGN KEY(actor_id) REFERENCES auth_users(id)
);
CREATE TABLE IF NOT EXISTS core_student_import_batches (
 draft_id CHAR(32) NOT NULL,
 batch_index INT NOT NULL,
 upload_session_id CHAR(32) NOT NULL,
 native_session_id BIGINT NULL,
 PRIMARY KEY(draft_id,batch_index),
 UNIQUE(upload_session_id),
 FOREIGN KEY(draft_id) REFERENCES core_student_import_drafts(id),
 FOREIGN KEY(native_session_id) REFERENCES core_imports(id)
);
