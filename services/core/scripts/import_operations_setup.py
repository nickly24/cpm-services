"""Guard/apply only additive migration 007 in fixed loopback cpm_dev.

Without --run: no credentials, DB connection, DDL or extension-manifest writes.
The coordinator runs --check/--apply --run only in the coordinated window.
No imports of dev_setup, seeds, scope backfills, or other migration runners.
"""
import argparse
from collections import defaultdict
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import sys

ROOT = Path(__file__).resolve().parents[3]
MIGRATION = ROOT / "services/core/migrations/007_core_import_operation_proofs.sql"
TABLES = {
    "core_import_operation_sources": {
        "kind": "enum('users.credentials','users.legacy')", "session_id": "bigint",
        "organization_id": "bigint", "division_id": "bigint", "actor_id": "int",
        "source_sha256": "char(64)", "provenance": "enum('core-file-v1','uploads-file-v1')",
    },
    "core_import_operation_receipts": {
        "organization_id": "bigint", "division_id": "bigint", "actor_id": "int",
        "command_key": "char(36)", "kind": "enum('users.credentials','users.legacy')",
        "session_id": "bigint", "command_hash": "char(64)", "source_sha256": "char(64)",
        "proof": "json", "response": "json", "http_status": "smallint", "created_at": "timestamp(6)",
    },
}
INDEXES = {
    "core_import_operation_sources": {
        (0, ("kind", "session_id")), (1, ("organization_id", "division_id", "actor_id")),
        (1, ("actor_id",)),
    },
    "core_import_operation_receipts": {
        (0, ("organization_id", "division_id", "actor_id", "command_key")),
        (1, ("kind", "session_id")), (1, ("actor_id",)),
    },
}
FKS = {
    (("organization_id", "division_id"), "divisions", ("organization_id", "id")),
    (("actor_id",), "auth_users", ("id",)),
}


class SetupError(Exception):
    pass


def columns(cursor, table):
    cursor.execute("SELECT column_name AS column_name,column_type AS column_type,"
                   "is_nullable AS is_nullable,column_default AS column_default,"
                   "collation_name AS collation_name FROM information_schema.columns "
                   "WHERE table_schema=DATABASE() AND table_name=%s", (table,))
    return cursor.fetchall()


def assert_table(cursor, table):
    cursor.execute("SELECT table_type AS table_type,engine AS engine FROM information_schema.tables "
                   "WHERE table_schema=DATABASE() AND table_name=%s", (table,))
    meta = cursor.fetchone()
    if meta is None:
        return False
    if meta["table_type"] != "BASE TABLE" or meta["engine"] != "InnoDB":
        raise SetupError("unknown_operation_table")
    rows = columns(cursor, table)
    if {row["column_name"] for row in rows} != set(TABLES[table]):
        raise SetupError("unknown_operation_columns")
    for row in rows:
        name = row["column_name"]
        # MySQL 5.7 display widths do not change signed numeric semantics.
        typ = re.sub(r"^(bigint|int|smallint)\([0-9]+\)$", r"\1", row["column_type"].lower())
        default = row["column_default"]
        if (typ != TABLES[table][name] or row["is_nullable"] != "NO"
                or (name != "created_at" and default is not None)
                or (name == "created_at" and str(default).lower() != "current_timestamp(6)")
                or (typ.startswith("char(") and row["collation_name"] != "ascii_bin")):
            raise SetupError("unknown_operation_column_type")
    cursor.execute("SELECT index_name AS index_name,non_unique AS non_unique,"
                   "seq_in_index AS seq_in_index,column_name AS column_name,sub_part AS sub_part "
                   "FROM information_schema.statistics WHERE table_schema=DATABASE() "
                   "AND table_name=%s ORDER BY index_name,seq_in_index", (table,))
    indexes = defaultdict(list)
    unique = {}
    for row in cursor.fetchall():
        if row["sub_part"] is not None:
            raise SetupError("operation_prefix_index_rejected")
        key = row["index_name"]
        indexes[key].append(row["column_name"])
        unique[key] = int(row["non_unique"])
    normalized = {(unique[key], tuple(value)) for key, value in indexes.items()}
    if (normalized != INDEXES[table] or len(indexes) != len(INDEXES[table])
            or "PRIMARY" not in indexes or unique["PRIMARY"] != 0):
        raise SetupError("unknown_operation_indexes")
    cursor.execute("SELECT k.constraint_name AS constraint_name,k.column_name AS column_name,"
                   "k.referenced_table_name AS referenced_table_name,"
                   "k.referenced_table_schema AS referenced_table_schema,"
                   "k.referenced_column_name AS referenced_column_name,"
                   "r.update_rule AS update_rule,r.delete_rule AS delete_rule "
                   "FROM information_schema.key_column_usage k "
                   "JOIN information_schema.referential_constraints r "
                   "ON r.constraint_schema=k.constraint_schema AND r.constraint_name=k.constraint_name "
                   "AND r.table_name=k.table_name "
                   "WHERE k.table_schema=DATABASE() AND k.table_name=%s "
                   "AND k.referenced_table_name IS NOT NULL ORDER BY k.constraint_name,k.ordinal_position", (table,))
    fks = defaultdict(list)
    for row in cursor.fetchall():
        if row["referenced_table_schema"] != "cpm_dev":
            raise SetupError("operation_foreign_database_rejected")
        if row["update_rule"] not in {"RESTRICT", "NO ACTION"} or row["delete_rule"] not in {"RESTRICT", "NO ACTION"}:
            raise SetupError("operation_cascade_rejected")
        fks[row["constraint_name"]].append(row)
    normalized_fks = {(tuple(row["column_name"] for row in rows), rows[0]["referenced_table_name"],
                       tuple(row["referenced_column_name"] for row in rows)) for rows in fks.values()}
    if normalized_fks != FKS or len(fks) != len(FKS):
        raise SetupError("unknown_operation_foreign_keys")
    return True


def preflight(cursor):
    cursor.execute("SELECT DATABASE() AS database_name")
    if cursor.fetchone()["database_name"] != "cpm_dev":
        raise SetupError("fixed_dev_database_required")
    # Verify native prerequisites, but never apply older migrations or backfill.
    for table, required in {
        "schools": {"organization_id"}, "divisions": {"id", "organization_id"},
        "auth_users": {"id"}, "auth_account_state": {"user_id", "auth_version", "is_active"},
        "core_imports": {"id", "payload", "status"},
        "core_legacy_imports": {"id", "revision", "status", "preview", "expires_at"},
    }.items():
        if not required.issubset({row["column_name"] for row in columns(cursor, table)}):
            raise SetupError("native_migration_prerequisites_missing")
    return {table: assert_table(cursor, table) for table in TABLES}


def migration_statements():
    source = MIGRATION.read_text()
    sql = "\n".join(line for line in source.splitlines() if not line.lstrip().startswith("--"))
    result = {}
    for statement in sql.split(";"):
        if not statement.strip():
            continue
        match = re.match(r"^CREATE TABLE IF NOT EXISTS (core_import_operation_[a-z]+) \(", statement.strip())
        if not match or match[1] not in TABLES or match[1] in result:
            raise SetupError("unexpected_migration_statement")
        result[match[1]] = statement.strip()
    if set(result) != set(TABLES):
        raise SetupError("migration_tables_mismatch")
    return result


def apply(cursor):
    existing = preflight(cursor)  # All existing tables checked before any DDL.
    statements = migration_statements()
    created = []
    for table in TABLES:
        if not existing[table]:
            cursor.execute(statements[table])
            created.append(table)
    for table in TABLES:
        if not assert_table(cursor, table):
            raise SetupError("operation_schema_not_ready")
    return created


def register_extensions(path):
    with path.open("r+") as stream:
        fcntl.flock(stream, fcntl.LOCK_EX)
        current = json.load(stream)
        if not isinstance(current, list) or not all(isinstance(name, str) for name in current):
            raise SetupError("unknown_extensions_manifest")
        updated = sorted(set(current).union(TABLES))
        if current != updated:
            stream.seek(0)
            json.dump(updated, stream, indent=2)
            stream.write("\n")
            stream.truncate()
            stream.flush()
            os.fsync(stream.fileno())


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=["check", "apply"])
    parser.add_argument("--run", action="store_true")
    args = parser.parse_args()
    if not args.run:
        print(json.dumps({"status": "prepared", "migration": "007", "action": args.action,
                          "migration_sha256": hashlib.sha256(MIGRATION.read_bytes()).hexdigest(),
                          "private_files_read": False, "database_connected": False, "runtime_control": False}))
        return
    # Delayed imports: preparing the helper never touches credentials or DB.
    sys.path.insert(0, str(ROOT / "dev/scripts"))
    from bootstrap import mysql_connection, settings
    connection = None
    cursor = None
    locked = False
    try:
        connection = mysql_connection(settings())
        cursor = connection.cursor(dictionary=True)
        cursor.execute("SELECT GET_LOCK('cpm_dev_core_import_operations_007',0) AS acquired")
        if cursor.fetchone()["acquired"] != 1:
            raise SetupError("migration_already_running")
        locked = True
        if args.action == "check":
            existing = preflight(cursor)
            result = {"status": "ready" if all(existing.values()) else "pending",
                      "migration": "007", "tables": existing, "database_writes": False}
        else:
            created = apply(cursor)
            connection.commit()
            register_extensions(ROOT / "dev/schema/extensions.json")
            result = {"status": "applied", "migration": "007", "created_tables": created,
                      "existing_data_retained": True, "backfill": False, "runtime_control": False}
    except Exception as error:
        code = str(error) if isinstance(error, SetupError) else "migration_unavailable"
        print(json.dumps({"status": "failed", "migration": "007", "code": code}), file=sys.stderr)
        raise SystemExit(1) from None
    finally:
        try:
            if cursor:
                if locked:
                    try:
                        cursor.execute("SELECT RELEASE_LOCK('cpm_dev_core_import_operations_007') AS released")
                        cursor.fetchone()
                    except Exception:
                        pass
                cursor.close()
        finally:
            if connection:
                connection.close()
    print(json.dumps(result))


if __name__ == "__main__":
    main()
