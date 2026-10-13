"""Run exams Go checks; optionally include guarded real-local HTTP regression."""
import argparse
from pathlib import Path
import shutil
import subprocess
import sys

SERVICE = Path(__file__).resolve().parents[1]
ROOT = SERVICE.parents[1]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--http", action="store_true", help="exercise running local services with synthetic fixtures")
    parser.add_argument("--audit-http", action="store_true", help="exercise two-organization, BFF/live-auth, Moscow dates and paged import scenarios")
    parser.add_argument("--import-rows", type=int, default=251, choices=[251, 5000])
    parser.add_argument("--keep-fixtures", action="store_true")
    args = parser.parse_args()
    go = shutil.which("go") or str(Path.home() / ".local/bin/go")
    for command in [[go, "test", "-race", "./..."], [go, "vet", "./..."]]:
        result = subprocess.run(command, cwd=SERVICE, check=False)
        if result.returncode:
            return result.returncode
    if args.http:
        python = ROOT / "dev/.venv/bin/python"
        command = [str(python if python.exists() else Path(sys.executable)), str(SERVICE / "scripts/http_test.py")]
        if args.keep_fixtures:
            command.append("--keep-fixtures")
        result = subprocess.run(command, cwd=ROOT, check=False)
        if result.returncode:
            return result.returncode
    if args.audit_http:
        python = ROOT / "dev/.venv/bin/python"
        command = [str(python if python.exists() else Path(sys.executable)), str(SERVICE / "scripts/http_audit.py"), "--import-rows", str(args.import_rows)]
        if args.keep_fixtures:
            command.append("--keep-fixtures")
        return subprocess.run(command, cwd=ROOT, check=False).returncode
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
