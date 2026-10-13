-- Dev-only initial context projection. Core will own organizations and divisions later.
CREATE TABLE IF NOT EXISTS organizations (
 id BIGINT NOT NULL PRIMARY KEY,
 name VARCHAR(150) NOT NULL,
 is_active BOOLEAN NOT NULL DEFAULT TRUE
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS divisions (
 id BIGINT NOT NULL PRIMARY KEY,
 organization_id BIGINT NOT NULL,
 name VARCHAR(150) NOT NULL,
 is_active BOOLEAN NOT NULL DEFAULT TRUE,
 UNIQUE KEY uq_division_org (organization_id,id),
 FOREIGN KEY (organization_id) REFERENCES organizations(id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS auth_account_state (
 user_id INT NOT NULL PRIMARY KEY,
 is_active BOOLEAN NOT NULL DEFAULT TRUE,
 auth_version BIGINT NOT NULL DEFAULT 1,
 FOREIGN KEY (user_id) REFERENCES auth_users(id),
 CHECK (auth_version > 0)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS auth_organization_access (
 user_id INT NOT NULL,
 organization_id BIGINT NOT NULL,
 PRIMARY KEY (user_id,organization_id),
 FOREIGN KEY (user_id) REFERENCES auth_users(id),
 FOREIGN KEY (organization_id) REFERENCES organizations(id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS auth_scoped_roles (
 id BIGINT NOT NULL PRIMARY KEY,
 organization_id BIGINT NOT NULL,
 name VARCHAR(100) NOT NULL,
 UNIQUE KEY uq_scoped_role_name (organization_id,name),
 UNIQUE KEY uq_scoped_role_org (organization_id,id),
 FOREIGN KEY (organization_id) REFERENCES organizations(id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS auth_scoped_role_permissions (
 role_id BIGINT NOT NULL,
 section VARCHAR(32) NOT NULL,
 can_view BOOLEAN NOT NULL DEFAULT FALSE,
 can_edit BOOLEAN NOT NULL DEFAULT FALSE,
 PRIMARY KEY (role_id,section),
 FOREIGN KEY (role_id) REFERENCES auth_scoped_roles(id),
 CHECK (can_view IN (0,1) AND can_edit IN (0,1) AND can_edit <= can_view)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS auth_division_access (
 user_id INT NOT NULL,
 organization_id BIGINT NOT NULL,
 division_id BIGINT NOT NULL,
 role_id BIGINT NULL,
 group_id INT NULL,
 PRIMARY KEY (user_id,division_id),
 FOREIGN KEY (user_id,organization_id) REFERENCES auth_organization_access(user_id,organization_id),
 FOREIGN KEY (organization_id,division_id) REFERENCES divisions(organization_id,id),
 FOREIGN KEY (organization_id,role_id) REFERENCES auth_scoped_roles(organization_id,id),
 FOREIGN KEY (group_id) REFERENCES `groups`(id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS auth_sessions (
 id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
 user_id INT NOT NULL,
 organization_id BIGINT NOT NULL,
 division_id BIGINT NULL,
 auth_version BIGINT NOT NULL,
 session_version BIGINT NOT NULL DEFAULT 0,
 expires_at DATETIME(6) NOT NULL,
 revoked_at DATETIME(6) NULL,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 FOREIGN KEY (user_id) REFERENCES auth_users(id),
 FOREIGN KEY (organization_id) REFERENCES organizations(id),
 FOREIGN KEY (organization_id,division_id) REFERENCES divisions(organization_id,id),
 KEY ix_session_expiry (expires_at)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS auth_refresh_tokens (
 token_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
 session_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 used_at DATETIME(6) NULL,
 FOREIGN KEY (session_id) REFERENCES auth_sessions(id),
 KEY ix_refresh_session (session_id)
) ENGINE=InnoDB;
