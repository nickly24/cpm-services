-- Immutable legacy attachments retained by attendance itself. No Uploads/S3
-- relocation, no foreign service ownership, no rows or keys inferred at runtime.
CREATE TABLE IF NOT EXISTS attendance_attachments_v1 (
 organization_id BIGINT NOT NULL,
 id BIGINT NOT NULL,
 zap_id BIGINT NOT NULL,
 mime VARCHAR(100) NOT NULL,
 content LONGBLOB NOT NULL,
 sha256 CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 PRIMARY KEY(organization_id,id),
 KEY attendance_attachment_statement(organization_id,zap_id,id)
) ENGINE=InnoDB;
