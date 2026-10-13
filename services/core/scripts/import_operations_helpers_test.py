"""Isolated helper tests: temp synthetic files/fake metadata; never Dev secrets/DB."""
import copy
import importlib.machinery
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import types
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(Path(__file__).parent))
import import_operations_setup as migration

loader = importlib.machinery.SourceFileLoader("core_operation_deploy", str(ROOT / "dev/core-operations"))
spec = importlib.util.spec_from_loader(loader.name, loader)
deploy = importlib.util.module_from_spec(spec)
loader.exec_module(deploy)


def credentials():
    # Distinct non-secret synthetic tokens; no actual provisioner/file read.
    return {key: str(index + 1) * 32 for index, key in enumerate(deploy.CANONICAL_KEYS)}


def fixture_schema(table):
    cols = [{"column_name": name, "column_type": typ, "is_nullable": "NO",
             "column_default": "current_timestamp(6)" if name == "created_at" else None,
             "collation_name": "ascii_bin" if typ.startswith("char(") else None}
            for name, typ in migration.TABLES[table].items()]
    indexes = []
    for ordinal, (non_unique, fields) in enumerate(sorted(migration.INDEXES[table])):
        name = "PRIMARY" if non_unique == 0 else f"secondary_{ordinal}"
        indexes.extend({"index_name": name, "non_unique": non_unique, "seq_in_index": seq,
                        "column_name": field, "sub_part": None}
                       for seq, field in enumerate(fields, 1))
    fks = []
    for index, (fields, referenced, target_fields) in enumerate(sorted(migration.FKS)):
        fks.extend({"constraint_name": f"fk_{index}", "column_name": field,
                    "referenced_table_schema": "cpm_dev", "referenced_table_name": referenced,
                    "referenced_column_name": target, "update_rule": "RESTRICT", "delete_rule": "RESTRICT"}
                   for field, target in zip(fields, target_fields))
    return {"meta": {"table_type": "BASE TABLE", "engine": "InnoDB"},
            "columns": cols, "indexes": indexes, "fks": fks}


class FakeCursor:
    def __init__(self, schemas=None):
        self.schemas = schemas or {}
        self.result = []
        self.ddl = []

    def execute(self, sql, args=()):
        if sql.startswith("SELECT DATABASE()"):
            self.result = [{"database_name": "cpm_dev"}]
        elif "information_schema" in sql:
            table = args[0]
            if table in migration.TABLES:
                schema = self.schemas.get(table)
                key = "meta" if "information_schema.tables" in sql else (
                    "columns" if "information_schema.columns" in sql else (
                        "indexes" if "information_schema.statistics" in sql else "fks"))
                self.result = [] if schema is None else ([schema[key]] if key == "meta" else schema[key])
            else:
                self.result = [{"column_name": field} for field in
                               {"id", "user_id", "organization_id", "auth_version", "is_active",
                                "payload", "status", "revision", "preview", "expires_at"}]
        elif sql.startswith("CREATE TABLE"):
            table = sql.split()[5]
            self.ddl.append(table)
            self.schemas[table] = fixture_schema(table)
        else:
            raise AssertionError("Unexpected query")

    def fetchone(self):
        return self.result[0] if self.result else None

    def fetchall(self):
        return copy.deepcopy(self.result)


class CredentialReaderTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.path = Path(self.temp.name) / "synthetic.env"

    def write(self, values):
        self.path.write_text("\n".join(f"{key}={value}" for key, value in values.items()) + "\n")
        self.path.chmod(0o600)

    def test_exact_core_subset_and_alias(self):
        values = credentials()
        self.write(values)
        actual = deploy.read_credentials(self.path, {}, "existing-synthetic-internal-token")
        self.assertEqual(set(actual), set(deploy.CORE_KEYS))
        self.assertEqual(actual[deploy.ALIAS], values["CARDS_CORE_OPERATION_KEY"])
        values[deploy.ALIAS] = values["CARDS_CORE_OPERATION_KEY"]
        self.write(values)
        self.assertEqual(actual, deploy.read_credentials(self.path, {}, "existing-synthetic-internal-token"))

    def test_invalid_values_and_overrides_fail_closed(self):
        for kind in ["missing", "duplicate-value", "alias", "unknown", "short", "expansion", "duplicate-key"]:
            with self.subTest(kind=kind):
                values = credentials()
                if kind == "missing": values.pop(deploy.CANONICAL_KEYS[0])
                if kind == "duplicate-value": values[deploy.CANONICAL_KEYS[0]] = values[deploy.CANONICAL_KEYS[1]]
                if kind == "alias": values[deploy.ALIAS] = "x" * 64
                if kind == "unknown": values["AUTH_INTERNAL_KEY"] = "x" * 64
                if kind == "short": values[deploy.CANONICAL_KEYS[0]] = "x"
                if kind == "expansion": values[deploy.CANONICAL_KEYS[0]] = "$(private-command)" + "x" * 32
                self.write(values)
                if kind == "duplicate-key":
                    with self.path.open("a") as stream: stream.write(f"{deploy.CANONICAL_KEYS[0]}={'x' * 64}\n")
                with self.assertRaises(deploy.DeploymentError):
                    deploy.read_credentials(self.path, {}, "existing-synthetic-internal-token")
        self.write(credentials())
        for key in set(deploy.CANONICAL_KEYS) | {deploy.ALIAS}:
            with self.subTest(override=key), self.assertRaises(deploy.DeploymentError):
                deploy.read_credentials(self.path, {key: ""}, "existing-synthetic-internal-token")
        with self.assertRaises(deploy.DeploymentError):
            deploy.read_credentials(self.path, {}, credentials()[deploy.CANONICAL_KEYS[0]])

    def test_file_security(self):
        self.write(credentials())
        self.path.chmod(0o644)
        with self.assertRaises(deploy.DeploymentError): deploy.private_text(self.path)
        self.path.chmod(0o600)
        link = self.path.with_name("link.env")
        link.symlink_to(self.path)
        with self.assertRaises(deploy.DeploymentError): deploy.private_text(link)
        with self.assertRaises(deploy.DeploymentError): deploy.private_text(self.path.parent)
        self.path.write_bytes(b"x" * 32769)
        with self.assertRaises(deploy.DeploymentError): deploy.private_text(self.path)

    def test_no_run_never_reads_secrets_or_starts_docker(self):
        with patch.object(deploy, "private_text", side_effect=AssertionError("Unexpected secret read")), \
                patch.object(deploy.subprocess, "run", side_effect=AssertionError("Unexpected runtime control")), \
                patch.object(sys, "argv", ["core-operations", "up", "--image", "sha256:" + "a" * 64]), \
                patch("builtins.print") as output:
            deploy.main()
        self.assertFalse(json.loads(output.call_args.args[0])["private_files_read"])

    def test_deploy_targets_only_immutable_core_without_build_or_dependencies(self):
        values = credentials()
        text = "\n".join(f"{key}={value}" for key, value in values.items())
        baseline = "MYSQL_DATABASE=cpm_dev\nMYSQL_PORT=13306\nAUTH_INTERNAL_KEY=existing-synthetic-internal-token\n"
        image = "sha256:" + "a" * 64
        def private(path): return baseline if path.name == ".env" else text
        calls = []
        def run(command, **kwargs):
            calls.append((command, kwargs))
            return subprocess.CompletedProcess(command, 0, image + "\n" if command[1] == "image" else "", "")
        with patch.object(deploy, "private_text", side_effect=private), patch.object(deploy.subprocess, "run", side_effect=run):
            result = deploy.deploy(image, {})
        self.assertEqual(result["service"], "core")
        command, kwargs = calls[1]
        self.assertEqual(command[-1], "core")
        self.assertEqual([command[index + 1] for index, value in enumerate(command) if value == "--profile"],
                         ["core", "auth"])
        self.assertEqual(command[command.index("up") + 1:],
                         ["-d", "--no-deps", "--no-build", "--pull", "never", "--wait", "--wait-timeout", "90", "core"])
        for option in ["--no-deps", "--no-build", "never"]: self.assertIn(option, command)
        self.assertEqual(set(kwargs["env"]), set(deploy.CORE_KEYS) | {"CORE_OPERATIONS_IMAGE"})
        self.assertEqual(kwargs["env"]["CORE_OPERATIONS_IMAGE"], image)


class MigrationGuardTests(unittest.TestCase):
    def test_exact_existing_schema_is_idempotent(self):
        cursor = FakeCursor({table: fixture_schema(table) for table in migration.TABLES})
        self.assertEqual(migration.apply(cursor), [])
        self.assertEqual(cursor.ddl, [])

    def test_absent_or_interrupted_tables_create_only_missing_007_tables(self):
        for retained in [None, *migration.TABLES]:
            with self.subTest(retained=retained):
                schemas = {} if retained is None else {retained: fixture_schema(retained)}
                cursor = FakeCursor(schemas)
                created = migration.apply(cursor)
                self.assertEqual(set(created), set(migration.TABLES) - ({retained} if retained else set()))
                self.assertEqual(created, cursor.ddl)

    def test_unknown_existing_schema_is_rejected_before_any_ddl(self):
        table = "core_import_operation_receipts"
        for kind in ["column", "nullable", "type", "collation", "default", "index", "prefix", "cascade", "foreign-db", "engine", "view"]:
            with self.subTest(kind=kind):
                schema = fixture_schema(table)
                if kind == "column": schema["columns"].append({"column_name": "unrelated"})
                if kind == "nullable": schema["columns"][0]["is_nullable"] = "YES"
                if kind == "type": schema["columns"][0]["column_type"] = "bigint unsigned"
                if kind == "collation": next(r for r in schema["columns"] if r["column_name"] == "command_key")["collation_name"] = "ascii_general_ci"
                if kind == "default": next(r for r in schema["columns"] if r["column_name"] == "created_at")["column_default"] = None
                if kind == "index": schema["indexes"].pop()
                if kind == "prefix": schema["indexes"][0]["sub_part"] = 8
                if kind == "cascade": schema["fks"][0]["delete_rule"] = "CASCADE"
                if kind == "foreign-db": schema["fks"][0]["referenced_table_schema"] = "foreign"
                if kind == "engine": schema["meta"]["engine"] = "MyISAM"
                if kind == "view": schema["meta"]["table_type"] = "VIEW"
                cursor = FakeCursor({table: schema})
                with self.assertRaises(migration.SetupError): migration.apply(cursor)
                self.assertEqual(cursor.ddl, [])

    def test_extension_registration_preserves_neighbours_and_is_idempotent(self):
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder) / "extensions.json"
            path.write_text('["unrelated_owner", "attendance_history"]\n')
            migration.register_extensions(path)
            original = path.read_bytes()
            self.assertEqual(set(json.loads(original)), {"unrelated_owner", "attendance_history"} | set(migration.TABLES))
            migration.register_extensions(path)
            self.assertEqual(path.read_bytes(), original)


class MigrationCleanupTests(unittest.TestCase):
    class Cursor:
        def __init__(self, close_error=False):
            self.unread = False
            self.closed = False
            self.close_error = close_error
            self.released = False

        def execute(self, sql):
            if self.unread:
                raise RuntimeError("Unread result found")
            if "GET_LOCK(" in sql:
                self.result = {"acquired": 1}
            elif "RELEASE_LOCK(" in sql:
                self.result = {"released": 1}
                self.released = True
            else:
                raise AssertionError("Unexpected query")
            self.unread = True

        def fetchone(self):
            self.unread = False
            return self.result

        def close(self):
            if self.unread:
                raise RuntimeError("Unread result found")
            self.closed = True
            if self.close_error:
                raise RuntimeError("Synthetic cursor cleanup failure")

    class Connection:
        def __init__(self, cursor):
            self.fixture_cursor = cursor
            self.committed = False
            self.closed = False

        def cursor(self, *, dictionary):
            assert dictionary is True
            return self.fixture_cursor

        def commit(self):
            self.committed = True

        def close(self):
            self.closed = True

    def invoke(self, action, cursor):
        connection = self.Connection(cursor)
        bootstrap = types.ModuleType("bootstrap")
        bootstrap.settings = lambda: {}
        bootstrap.mysql_connection = lambda _: connection
        with patch.dict(sys.modules, {"bootstrap": bootstrap}), \
                patch.object(sys, "argv", ["import_operations_setup.py", action, "--run"]), \
                patch.object(migration, "preflight", return_value={table: True for table in migration.TABLES}), \
                patch.object(migration, "apply", return_value=[]), \
                patch.object(migration, "register_extensions"), patch("builtins.print") as output:
            try:
                migration.main()
            finally:
                self.assertTrue(connection.closed)
        return connection, json.loads(output.call_args.args[0])

    def test_successful_apply_consumes_release_result_before_close(self):
        cursor = self.Cursor()
        connection, result = self.invoke("apply", cursor)
        self.assertTrue(connection.committed)
        self.assertEqual(result["status"], "applied")
        self.assertEqual(result["created_tables"], [])
        self.assertTrue(cursor.released and cursor.closed)
        self.assertFalse(cursor.unread)

    def test_successful_check_consumes_release_result_before_close(self):
        cursor = self.Cursor()
        connection, result = self.invoke("check", cursor)
        self.assertFalse(connection.committed)
        self.assertEqual(result["status"], "ready")
        self.assertFalse(result["database_writes"])
        self.assertTrue(cursor.released and cursor.closed)
        self.assertFalse(cursor.unread)

    def test_connection_closes_even_when_cursor_cleanup_fails(self):
        with self.assertRaisesRegex(RuntimeError, "Synthetic cursor cleanup failure"):
            self.invoke("check", self.Cursor(close_error=True))


if __name__ == "__main__":
    unittest.main()
