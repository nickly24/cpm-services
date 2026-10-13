ALTER TABLE core_direction_scope DROP FOREIGN KEY core_direction_scope_ibfk_2;
ALTER TABLE core_direction_scope ADD CONSTRAINT fk_core_direction_organization FOREIGN KEY (organization_id) REFERENCES organizations(id);
ALTER TABLE core_direction_scope DROP COLUMN division_id;
