CREATE TABLE IF NOT EXISTS cards_themes_v1 (
 id BIGINT AUTO_INCREMENT PRIMARY KEY, organization_id BIGINT NOT NULL, direction_id INT NOT NULL,
 name VARCHAR(200) NOT NULL, revision BIGINT NOT NULL DEFAULT 1,
 UNIQUE(organization_id,direction_id,name), UNIQUE(organization_id,id),
 FOREIGN KEY(organization_id) REFERENCES organizations(id), INDEX(organization_id,direction_id)
);
CREATE TABLE IF NOT EXISTS cards_items_v1 (
 id BIGINT AUTO_INCREMENT PRIMARY KEY, organization_id BIGINT NOT NULL, theme_id BIGINT NOT NULL,
 question TEXT NOT NULL, answer TEXT NOT NULL, sort_order BIGINT NOT NULL, revision BIGINT NOT NULL DEFAULT 1,
 FOREIGN KEY(organization_id,theme_id) REFERENCES cards_themes_v1(organization_id,id) ON DELETE CASCADE,
 INDEX(organization_id,theme_id,sort_order,id)
);
CREATE TABLE IF NOT EXISTS cards_progress_v1 (
 account_id INT NOT NULL, organization_id BIGINT NOT NULL, section_kind VARCHAR(8) NOT NULL,
 section_ref VARCHAR(64) NOT NULL, card_ref VARCHAR(100) NOT NULL, fingerprint CHAR(64) NOT NULL,
 learned_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
 PRIMARY KEY(account_id,organization_id,card_ref), INDEX(organization_id,section_kind,section_ref),
 FOREIGN KEY(account_id) REFERENCES auth_users(id), FOREIGN KEY(organization_id) REFERENCES organizations(id)
);
CREATE TABLE IF NOT EXISTS cards_settings_v1 (
 account_id INT NOT NULL, organization_id BIGINT NOT NULL, section_kind VARCHAR(8) NOT NULL,
 section_ref VARCHAR(64) NOT NULL, batch_size INT NOT NULL DEFAULT 10, study_mode VARCHAR(12) NOT NULL DEFAULT 'unlearned', last_batch_index INT NULL,
 PRIMARY KEY(account_id,organization_id,section_kind,section_ref), FOREIGN KEY(account_id) REFERENCES auth_users(id), FOREIGN KEY(organization_id) REFERENCES organizations(id),
 CHECK(batch_size IN (10,20,30)), CHECK(study_mode IN ('all','unlearned','learned','stale')), CHECK(last_batch_index IS NULL OR last_batch_index>=0)
);
CREATE TABLE IF NOT EXISTS cards_sessions_v1 (
 id CHAR(32) PRIMARY KEY, organization_id BIGINT NOT NULL, account_id INT NOT NULL, kind VARCHAR(12) NOT NULL,
 division_id BIGINT NULL, direction_id INT NOT NULL, revision BIGINT NOT NULL DEFAULT 1,
 state VARCHAR(12) NOT NULL DEFAULT 'preview', preview JSON NOT NULL, result JSON NULL,
 created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP, expires_at TIMESTAMP NOT NULL,
 INDEX(organization_id,account_id,expires_at), FOREIGN KEY(organization_id) REFERENCES organizations(id), FOREIGN KEY(account_id) REFERENCES auth_users(id)
);
