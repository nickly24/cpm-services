"""Apply only additive Core migration 006 to fixed local cpm_dev.

No resets, fixture rewriting, env edits or runtime control. Coordinator runs this
after 005. CREATE IF NOT EXISTS makes interrupted DDL application resumable.
"""
import fcntl
import json
import os
from pathlib import Path
import sys

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / "dev/scripts"))
from bootstrap import mysql_connection, settings

TABLES = {
    "core_legacy_imports": {"id", "organization_id", "division_id", "actor_id", "revision", "status",
                            "payload", "preview", "report", "created_at", "expires_at"},
    "core_legacy_import_receipts": {"organization_id", "division_id", "actor_id", "command_key",
                                    "command_hash", "session_id", "response", "http_status", "created_at"},
    "core_import_receipts": {"organization_id", "division_id", "actor_id", "command_key",
                              "command_hash", "session_id", "created_at"},
}


def apply(cursor):
    cursor.execute("SELECT DATABASE() name")
    if cursor.fetchone()["name"] != "cpm_dev":
        raise RuntimeError("Only fixed cpm_dev is supported")
    cursor.execute("SELECT data_type AS data_type FROM information_schema.columns WHERE table_schema=DATABASE() "
                   "AND table_name='schools' AND column_name='organization_id'")
    school = cursor.fetchone()
    if not school or school["data_type"] != "bigint":
        raise RuntimeError("Apply coordinated migration 005 before legacy import")
    # Refuse an unrelated same-name table; never replace or alter existing data.
    for table, expected in TABLES.items():
        cursor.execute("SELECT column_name AS column_name FROM information_schema.columns WHERE table_schema=DATABASE() "
                       "AND table_name=%s", (table,))
        current = {row["column_name"] for row in cursor.fetchall()}
        if current and current != expected:
            raise RuntimeError("Unknown existing Core import schema; retained unchanged")
    migration = ROOT / "services/core/migrations/006_core_legacy_imports.sql"
    sql = "\n".join(line for line in migration.read_text().splitlines()
                    if not line.lstrip().startswith("--"))
    for statement in sql.split(";"):
        if statement.strip():
            cursor.execute(statement.strip())


def register_extensions():
    path = ROOT / "dev/schema/extensions.json"
    # Acquire the existing file lock, then read its current contents. Add only
    # our three tables; preserve every sibling table registered before this call.
    with path.open("r+") as stream:
        fcntl.flock(stream, fcntl.LOCK_EX)
        current = json.load(stream)
        if not isinstance(current, list) or not all(isinstance(name, str) for name in current):
            raise RuntimeError("Unknown extensions manifest; retained unchanged")
        merged = sorted(set(current).union(TABLES))
        stream.seek(0)
        json.dump(merged, stream, indent=2)
        stream.write("\n")
        stream.truncate()
        stream.flush()
        os.fsync(stream.fileno())


def main():
    connection = mysql_connection(settings())
    cursor = connection.cursor(dictionary=True)
    try:
        apply(cursor)
        connection.commit()
    except Exception:
        connection.rollback()
        raise
    finally:
        cursor.close()
        connection.close()
    register_extensions()
    print(json.dumps({"status": "applied", "migration": "006", "tables": sorted(TABLES),
                      "existing_data_retained": True, "runtime_control": False}))


if __name__ == "__main__":
    main()
