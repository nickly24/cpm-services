-- Preserve legacy school IDs and unowned rows. Only Core-owned rows receive an
-- organization; legacy callers can still retain nullable ownership.
ALTER TABLE schools ADD COLUMN organization_id BIGINT NULL;
UPDATE schools s JOIN core_school_scope x ON x.school_id=s.id
 SET s.organization_id=x.organization_id,s.updated_at=s.updated_at WHERE s.organization_id IS NULL;
ALTER TABLE schools
 ADD UNIQUE KEY uq_schools_organization_name (organization_id,name),
 ADD CONSTRAINT fk_core_school_organization FOREIGN KEY (organization_id) REFERENCES organizations(id);
-- The dev helper validates this exact legacy index before dropping it.
ALTER TABLE schools DROP INDEX uq_schools_name;

-- Legacy Allratings has no source organization/division. A reviewed migration
-- must write the original scope explicitly; never infer it from new memberships.
CREATE TABLE IF NOT EXISTS core_legacy_rating_scope (
 rating_id INT NOT NULL,
 user_id INT NOT NULL,
 organization_id BIGINT NOT NULL,
 division_id BIGINT NOT NULL,
 created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 PRIMARY KEY (rating_id,user_id),
 INDEX ix_core_legacy_rating_membership (organization_id,division_id,user_id),
 -- Keep the history reservation even if the legacy owner replaces its row.
 FOREIGN KEY (user_id) REFERENCES auth_users(id),
 FOREIGN KEY (organization_id,division_id) REFERENCES divisions(organization_id,id)
);
