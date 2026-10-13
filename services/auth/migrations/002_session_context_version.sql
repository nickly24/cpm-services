-- Apply once through scripts/migrate_context_version.py (idempotent runner).
-- Old access tokens have no context_version and must be refreshed after rollout.
ALTER TABLE auth_sessions ADD COLUMN context_version BIGINT NOT NULL DEFAULT 1;
