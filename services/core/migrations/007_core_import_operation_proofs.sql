-- Additive background-import proofs. Existing previews are deliberately not
-- backfilled: an unknown original source cannot authorize a new operation.
CREATE TABLE IF NOT EXISTS core_import_operation_sources (
 kind ENUM('users.credentials','users.legacy') NOT NULL,
 session_id BIGINT NOT NULL,
 organization_id BIGINT NOT NULL, division_id BIGINT NOT NULL, actor_id INT NOT NULL,
 source_sha256 CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 provenance ENUM('core-file-v1','uploads-file-v1') NOT NULL,
 PRIMARY KEY(kind,session_id),
 FOREIGN KEY(organization_id,division_id) REFERENCES divisions(organization_id,id),
 FOREIGN KEY(actor_id) REFERENCES auth_users(id),
 INDEX(organization_id,division_id,actor_id)
) ENGINE=InnoDB;

-- A credential creation receipt and an application receipt are different
-- commands. These historical receipts pin the proof used for that application;
-- readonly recovery never revalidates applied rows as new duplicates.
CREATE TABLE IF NOT EXISTS core_import_operation_receipts (
 organization_id BIGINT NOT NULL, division_id BIGINT NOT NULL, actor_id INT NOT NULL,
 command_key CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 kind ENUM('users.credentials','users.legacy') NOT NULL, session_id BIGINT NOT NULL,
 command_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 source_sha256 CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 proof JSON NOT NULL, response JSON NOT NULL, http_status SMALLINT NOT NULL,
 created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 PRIMARY KEY(organization_id,division_id,actor_id,command_key),
 FOREIGN KEY(organization_id,division_id) REFERENCES divisions(organization_id,id),
 FOREIGN KEY(actor_id) REFERENCES auth_users(id),
 INDEX(kind,session_id)
) ENGINE=InnoDB;
