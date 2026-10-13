ALTER TABLE exams_imports_v1 ADD COLUMN source_filename VARCHAR(255) NOT NULL DEFAULT '';
ALTER TABLE exams_imports_v1 ADD COLUMN source_hash CHAR(64) NOT NULL DEFAULT '';
ALTER TABLE exams_receipts_v1 ADD INDEX exams_receipts_retention(status_code,created_at);
