-- JSON storage changes number lexemes. Keep canonical validated DTO bytes exact.
-- Existing hashes are deliberately not rewritten; a changed historical snapshot
-- must fail integrity checks and be recreated through an explicit user command.
SET @uploads_exact_preview_ddl = (SELECT IF(EXISTS (
 SELECT 1 FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE()
 AND TABLE_NAME='uploads_previews_v1' AND COLUMN_NAME IN ('input','preview') AND DATA_TYPE='json'
), 'ALTER TABLE uploads_previews_v1 MODIFY COLUMN input MEDIUMBLOB NOT NULL, MODIFY COLUMN preview MEDIUMBLOB NOT NULL', 'SELECT 1'));
PREPARE uploads_exact_preview_stmt FROM @uploads_exact_preview_ddl;
EXECUTE uploads_exact_preview_stmt;
DEALLOCATE PREPARE uploads_exact_preview_stmt;
