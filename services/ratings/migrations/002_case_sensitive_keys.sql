ALTER TABLE ratings_jobs_v1
 MODIFY COLUMN idempotency_key VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL;
