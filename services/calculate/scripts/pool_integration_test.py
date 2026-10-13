"""Run opt-in Go pool tests against disposable collections in local cpm_dev."""
import os
from pathlib import Path
import shutil
import subprocess
import sys
from urllib.parse import quote

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / "dev/scripts"))
from bootstrap import settings


def main():
    values = settings()
    go = shutil.which("go") or str(Path.home() / ".local/bin/go")
    env = dict(os.environ)
    env["CALCULATE_TEST_MONGO_URI"] = (
        "mongodb://" + quote(values["MONGO_USER"], safe="") + ":"
        + quote(values["MONGO_PASSWORD"], safe="")
        + "@127.0.0.1:27018/cpm_dev?authSource=cpm_dev"
    )
    # Connection credentials remain solely in the child environment; never
    # place them in command arguments or print the environment on failures.
    result = subprocess.run(
        [go, "test", "-race", "./internal/calculate", "-run", "^TestMongo", "-v", "-count=1"],
        cwd=ROOT / "services/calculate", env=env,
    )
    raise SystemExit(result.returncode)


if __name__ == "__main__":
    main()
