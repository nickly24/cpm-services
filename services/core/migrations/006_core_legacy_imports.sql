CREATE TABLE IF NOT EXISTS core_legacy_imports (
 id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
 organization_id BIGINT NOT NULL, division_id BIGINT NOT NULL, actor_id INT NOT NULL,
 revision BIGINT NOT NULL DEFAULT 1,
 status ENUM('preview','completed') NOT NULL DEFAULT 'preview',
 payload JSON NOT NULL, preview JSON NOT NULL, report JSON NULL,
 created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 expires_at TIMESTAMP(6) NOT NULL,
 FOREIGN KEY(organization_id,division_id) REFERENCES divisions(organization_id,id),
 FOREIGN KEY(actor_id) REFERENCES auth_users(id),
 INDEX(organization_id,division_id,actor_id,id)
);

-- Durable owner command receipts. No bearer, password or password hash is stored
-- here; response is the same non-secret DTO returned by the public owner API.
CREATE TABLE IF NOT EXISTS core_legacy_import_receipts (
 organization_id BIGINT NOT NULL, division_id BIGINT NOT NULL, actor_id INT NOT NULL,
 command_key CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 command_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 session_id BIGINT NOT NULL, response JSON NOT NULL, http_status SMALLINT NOT NULL,
 created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 PRIMARY KEY(organization_id,division_id,actor_id,command_key),
 FOREIGN KEY(session_id) REFERENCES core_legacy_imports(id),
 FOREIGN KEY(organization_id,division_id) REFERENCES divisions(organization_id,id),
 FOREIGN KEY(actor_id) REFERENCES auth_users(id)
);

-- Optional idempotency for the separate credential-based import. Only request
-- digest and session reference: credentials remain in its existing private store.
CREATE TABLE IF NOT EXISTS core_import_receipts (
 organization_id BIGINT NOT NULL, division_id BIGINT NOT NULL, actor_id INT NOT NULL,
 command_key CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 command_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 session_id BIGINT NOT NULL,
 created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 PRIMARY KEY(organization_id,division_id,actor_id,command_key),
 FOREIGN KEY(session_id) REFERENCES core_imports(id),
 FOREIGN KEY(organization_id,division_id) REFERENCES divisions(organization_id,id),
 FOREIGN KEY(actor_id) REFERENCES auth_users(id)
);
