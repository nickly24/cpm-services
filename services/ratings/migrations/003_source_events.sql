CREATE TABLE IF NOT EXISTS ratings_source_events_v1 (
 message_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
 payload_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 source VARCHAR(32) NOT NULL, organization_id BIGINT NOT NULL, division_id BIGINT NOT NULL,
 source_revision BIGINT NOT NULL, received_at DATETIME(6) NOT NULL,
 INDEX source_scope(source,organization_id,division_id,source_revision)
);
CREATE TABLE IF NOT EXISTS ratings_source_revisions_v1 (
 source VARCHAR(32) NOT NULL, organization_id BIGINT NOT NULL, division_id BIGINT NOT NULL,
 revision BIGINT NOT NULL, updated_at DATETIME(6) NOT NULL,
 PRIMARY KEY(source,organization_id,division_id)
);
