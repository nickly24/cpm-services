-- Dev enablement is explicitly authorized; execute only in an ACKed window.
-- Immutable metadata only: never bearer, password, source rows or wire bodies.
-- No cascading foreign keys: deleting source/account must not erase history.
CREATE TABLE IF NOT EXISTS auth_operation_grants (
 id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
 issuance_key CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 input_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 mode ENUM('apply','receipt') NOT NULL,
 parent_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NULL,
 account_id INT NOT NULL,
 organization_id BIGINT NOT NULL,
 division_id BIGINT NOT NULL,
 profile_id BIGINT NOT NULL,
 auth_version BIGINT NOT NULL,
 role VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 binding JSON NOT NULL,
 issued_at DATETIME(6) NOT NULL,
 expires_at DATETIME(6) NOT NULL,
 revoked BOOLEAN NOT NULL DEFAULT FALSE,
 UNIQUE KEY uq_operation_issuance (account_id,issuance_key),
 KEY ix_operation_actor_scope (account_id,organization_id,division_id),
 KEY ix_operation_expiry (expires_at),
 CHECK (account_id>0 AND organization_id>0 AND division_id>0 AND profile_id>0 AND auth_version>0),
 CHECK (revoked IN (0,1))
) ENGINE=InnoDB;
