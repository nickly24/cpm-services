-- Synthetic, connection-local fixture. This is not an application migration.
-- MySQL temporary tables cannot carry foreign keys. Primary/unique keys and
-- production column types used by the workflow remain enforced by MySQL.
CREATE TEMPORARY TABLE organizations (
 id BIGINT PRIMARY KEY, name VARCHAR(150) NOT NULL, is_active BOOLEAN NOT NULL DEFAULT TRUE
) ENGINE=InnoDB;
CREATE TEMPORARY TABLE divisions (
 id BIGINT PRIMARY KEY, organization_id BIGINT NOT NULL, name VARCHAR(150) NOT NULL,
 is_active BOOLEAN NOT NULL DEFAULT TRUE, UNIQUE KEY uq_division_org (organization_id,id)
) ENGINE=InnoDB;
CREATE TEMPORARY TABLE auth_users (
 id INT NOT NULL AUTO_INCREMENT PRIMARY KEY, username VARCHAR(50) NOT NULL UNIQUE,
 ref_id INT NOT NULL, password VARCHAR(255),
 role ENUM('student','proctor','admin','examinator','supervisor','staff_admin') NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
CREATE TEMPORARY TABLE students (
 id INT NOT NULL AUTO_INCREMENT PRIMARY KEY, full_name VARCHAR(50) NOT NULL,
 group_id INT, school_id INT, class INT, tg_name VARCHAR(30) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
CREATE TEMPORARY TABLE examinators (
 id INT NOT NULL AUTO_INCREMENT PRIMARY KEY, full_name VARCHAR(50) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
CREATE TEMPORARY TABLE auth_account_state (
 user_id INT PRIMARY KEY, is_active BOOLEAN NOT NULL DEFAULT TRUE,
 auth_version BIGINT NOT NULL DEFAULT 1, CHECK (auth_version > 0)
) ENGINE=InnoDB;
CREATE TEMPORARY TABLE core_user_owners (
 user_id INT NOT NULL PRIMARY KEY, organization_id BIGINT NOT NULL
) ENGINE=InnoDB;
CREATE TEMPORARY TABLE auth_organization_access (
 user_id INT NOT NULL, organization_id BIGINT NOT NULL, PRIMARY KEY(user_id,organization_id)
) ENGINE=InnoDB;
CREATE TEMPORARY TABLE auth_division_access (
 user_id INT NOT NULL, organization_id BIGINT NOT NULL, division_id BIGINT NOT NULL,
 role_id BIGINT, group_id INT, PRIMARY KEY(user_id,division_id)
) ENGINE=InnoDB;
CREATE TEMPORARY TABLE core_profile_extra (
 user_id INT PRIMARY KEY, tg_name VARCHAR(30) NOT NULL DEFAULT ''
) ENGINE=InnoDB;
CREATE TEMPORARY TABLE core_audit (
 id BIGINT AUTO_INCREMENT PRIMARY KEY, actor_id INT, organization_id BIGINT NOT NULL,
 division_id BIGINT, action VARCHAR(80) NOT NULL, entity_id BIGINT NOT NULL,
 created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP, INDEX(organization_id,id)
) ENGINE=InnoDB;
