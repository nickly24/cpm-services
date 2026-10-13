"""Apply migration 005 only to fixed synthetic cpm_dev, without deleting data.

MySQL DDL commits each statement. Metadata checks make this helper resumable;
unknown indexes, conflicting ownership or unknown legacy rating origins stop it.
Production requires its own reviewed original-division mapping.
"""
import argparse
import json
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / "dev/scripts"))
from bootstrap import mysql_connection, settings


def index(cur, name):
    cur.execute("SELECT column_name AS column_name,non_unique AS non_unique,sub_part AS sub_part FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='schools' AND index_name=%s ORDER BY seq_in_index", (name,))
    return cur.fetchall()


def expected_index(rows, columns):
    return len(rows) == len(columns) and all(
        row["column_name"] == column and row["non_unique"] == 0 and row["sub_part"] is None
        for row, column in zip(rows, columns)
    )


def apply(cur):
    cur.execute("SELECT DATABASE() name")
    if cur.fetchone()["name"] != "cpm_dev":
        raise RuntimeError("Only fixed cpm_dev is supported")
    legacy = index(cur, "uq_schools_name")
    if legacy and not expected_index(legacy, ["name"]):
        raise RuntimeError("Unknown legacy school index; retained unchanged")
    scoped = index(cur, "uq_schools_organization_name")
    if scoped and not expected_index(scoped, ["organization_id", "name"]):
        raise RuntimeError("Unknown scoped school index; retained unchanged")
    cur.execute("SELECT data_type AS data_type,is_nullable AS is_nullable FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='schools' AND column_name='organization_id'")
    column = cur.fetchone()
    if column and (column["data_type"] != "bigint" or column["is_nullable"] != "YES"):
        raise RuntimeError("Unknown school ownership column; retained unchanged")
    if column:
        cur.execute("SELECT COUNT(*) n FROM schools s JOIN core_school_scope x ON x.school_id=s.id WHERE s.organization_id IS NOT NULL AND s.organization_id<>x.organization_id")
        if cur.fetchone()["n"]:
            raise RuntimeError("Conflicting school ownership; retained unchanged")
    else:
        cur.execute("ALTER TABLE schools ADD COLUMN organization_id BIGINT NULL")
    cur.execute("UPDATE schools s JOIN core_school_scope x ON x.school_id=s.id SET s.organization_id=x.organization_id,s.updated_at=s.updated_at WHERE s.organization_id IS NULL")
    cur.execute("SELECT organization_id,name FROM schools WHERE organization_id IS NOT NULL GROUP BY organization_id,name HAVING COUNT(*)>1 LIMIT 1")
    if cur.fetchone():
        raise RuntimeError("Duplicate school name in organization; no records removed")
    if not scoped:
        cur.execute("ALTER TABLE schools ADD UNIQUE KEY uq_schools_organization_name (organization_id,name)")
    cur.execute("SELECT column_name AS column_name,referenced_table_name AS referenced_table_name,referenced_column_name AS referenced_column_name FROM information_schema.key_column_usage WHERE table_schema=DATABASE() AND table_name='schools' AND constraint_name='fk_core_school_organization'")
    foreign = cur.fetchall()
    if foreign and (len(foreign) != 1 or foreign[0] != {"column_name": "organization_id", "referenced_table_name": "organizations", "referenced_column_name": "id"}):
        raise RuntimeError("Unknown school organization constraint; retained unchanged")
    if not foreign:
        cur.execute("ALTER TABLE schools ADD CONSTRAINT fk_core_school_organization FOREIGN KEY (organization_id) REFERENCES organizations(id)")
    if legacy:
        cur.execute("ALTER TABLE schools DROP INDEX uq_schools_name")
    migration = (ROOT / "services/core/migrations/005_core_scope_guards.sql").read_text()
    cur.execute(migration[migration.index("CREATE TABLE IF NOT EXISTS core_legacy_rating_scope"):].strip().rstrip(";"))


def map_known_dev_ratings(cur):
    # The initial auth/Core synthetic seed explicitly imports legacy law users
    # into this division. Never derive this from MIN(current memberships).
    marker = ROOT / "dev/local/auth-seed-complete.json"
    if not marker.exists() or json.loads(marker.read_text()).get("divisions") != 4:
        raise RuntimeError("Known synthetic initial-scope marker is required")
    cur.execute("SELECT d.name division_name,o.name organization_name FROM divisions d JOIN organizations o ON o.id=d.organization_id WHERE d.id=1001 AND d.organization_id=1001")
    scope = cur.fetchone()
    if not scope or scope["division_name"] != "DEV Основная сборная" or not scope["organization_name"].startswith("DEV "):
        raise RuntimeError("Known original synthetic division must be reviewed")
    cur.execute("SELECT COUNT(*) n FROM Allratings")
    total = cur.fetchone()["n"]
    cur.execute("SELECT r.id rating_id,a.id user_id,a.username,o.organization_id,m.division_id FROM Allratings r LEFT JOIN auth_users a ON a.ref_id=r.student_id AND a.role='student' LEFT JOIN core_user_owners o ON o.user_id=a.id LEFT JOIN auth_division_access m ON m.user_id=a.id AND m.organization_id=1001 AND m.division_id=1001 ORDER BY r.id,a.id")
    rows = cur.fetchall()
    if len(rows) != total or any(not row["user_id"] or not row["username"].startswith("dev_") or row["organization_id"] != 1001 or row["division_id"] != 1001 for row in rows):
        raise RuntimeError("Unknown or ambiguous legacy rating origin; explicit reviewed mapping is required")
    for row in rows:
        cur.execute("SELECT organization_id,division_id FROM core_legacy_rating_scope WHERE rating_id=%s AND user_id=%s", (row["rating_id"], row["user_id"]))
        existing = cur.fetchone()
        if existing and existing != {"organization_id": 1001, "division_id": 1001}:
            raise RuntimeError("Existing reviewed legacy scope differs; retained unchanged")
    for row in rows:
        cur.execute("INSERT IGNORE INTO core_legacy_rating_scope(rating_id,user_id,organization_id,division_id) VALUES(%s,%s,1001,1001)", (row["rating_id"], row["user_id"]))
    return total


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--map-known-dev-ratings", action="store_true", help="Explicitly map only the reviewed initial synthetic law division")
    args = parser.parse_args()
    conn = mysql_connection(settings())
    cur = conn.cursor(dictionary=True)
    try:
        apply(cur)
        count = map_known_dev_ratings(cur) if args.map_known_dev_ratings else None
        conn.commit()
        print(json.dumps({"status": "applied", "migration": "005", "legacy_rating_scopes": count, "preserved_school_ids": True}))
    except Exception:
        conn.rollback()
        raise
    finally:
        cur.close()
        conn.close()


if __name__ == "__main__":
    main()
