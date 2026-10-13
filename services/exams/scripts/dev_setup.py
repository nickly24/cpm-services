"""Additive schema and private settings for the fixed local cpm_dev database."""
import json
import sys
import re
from urllib.parse import urlparse, unquote
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / "dev/scripts"))
from bootstrap import settings, mysql_connection


def ensure_schema(cursor):
    """Apply Exams-only additive migrations, checking existing DDL explicitly."""
    cursor.execute("SELECT DATABASE()")
    if cursor.fetchone()[0] != "cpm_dev":
        raise RuntimeError("Only cpm_dev is supported")
    for migration in sorted((ROOT / "services/exams/migrations").glob("*.sql")):
        for raw in migration.read_text().split(";"):
            statement = "\n".join(line for line in raw.splitlines() if not line.lstrip().startswith("--")).strip()
            if not statement:
                continue
            addition = re.match(r"ALTER TABLE ([a-z0-9_]+) ADD (COLUMN|INDEX) ([a-z0-9_]+)\b", statement, re.I)
            if addition:
                table, kind, name = addition.groups()
                if not table.startswith("exams_"):
                    raise RuntimeError("Migration outside Exams scope")
                catalogue = "COLUMNS" if kind.upper() == "COLUMN" else "STATISTICS"
                field = "COLUMN_NAME" if kind.upper() == "COLUMN" else "INDEX_NAME"
                cursor.execute(f"SELECT COUNT(*) FROM information_schema.{catalogue} WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=%s AND {field}=%s", (table, name))
                if cursor.fetchone()[0]:
                    continue
            cursor.execute(statement)


def main():
    values = settings()
    broker = urlparse(values.get("RABBITMQ_DOCKER_URL", ""))
    if broker.scheme != "amqp" or broker.hostname != "rabbitmq" or unquote(broker.path) != "/cpm_dev" or not broker.username or not broker.password or broker.query or broker.fragment:
        raise RuntimeError("Prepare shared local RabbitMQ cpm_dev settings first")
    connection = mysql_connection(values)
    try:
        cursor = connection.cursor()
        ensure_schema(cursor)
        connection.commit()
    finally:
        connection.close()
    local = ROOT / "dev/local"
    local.mkdir(mode=0o700, parents=True, exist_ok=True)
    env = {key: values[key] for key in ["MYSQL_USER", "MYSQL_PASSWORD", "AUTH_INTERNAL_KEY"]}
    env.update(MYSQL_HOST="127.0.0.1", MYSQL_PORT="13306", MYSQL_DATABASE="cpm_dev",
               EXAMS_LISTEN_ADDR="127.0.0.1:8010", EXAMS_AUTH_URL="http://127.0.0.1:8002",
               EXAMS_CORE_URL="http://127.0.0.1:8003", RABBITMQ_URL=values.get("RABBITMQ_URL", ""))
    path = local / "exams.env"
    path.touch(mode=0o600, exist_ok=True)
    path.chmod(0o600)
    path.write_text("\n".join(f"{key}={value}" for key, value in env.items()) + "\n")
    compose_path = local / "exams-compose.env"
    compose_path.touch(mode=0o600, exist_ok=True)
    compose_path.chmod(0o600)
    compose_path.write_text("RABBITMQ_DOCKER_URL=" + values["RABBITMQ_DOCKER_URL"] + "\n")
    print("Exams additive schema and private local configuration ready.")


if __name__ == "__main__":
    main()
