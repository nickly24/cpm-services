CREATE TABLE IF NOT EXISTS core_organization_test_settings (
 organization_id BIGINT NOT NULL PRIMARY KEY,
 recalculate_on_test_change BOOLEAN NOT NULL DEFAULT 1,
 revision BIGINT NOT NULL,
 updated_by INT NOT NULL,
 updated_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 FOREIGN KEY(organization_id) REFERENCES organizations(id),
 FOREIGN KEY(updated_by) REFERENCES auth_users(id),
 CHECK(revision > 0)
);
