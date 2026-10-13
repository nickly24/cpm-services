"""Launch the native local BFF with validated private credential files."""
import os
from pathlib import Path
import re
import subprocess
import sys

from gateway_env import prepare

BASE = Path(__file__).resolve().parents[1]
ROOT = BASE.parent


def main():
    prepare()
    subprocess.run([sys.executable, str(ROOT / "services/bff/scripts/redis_env.py")], check=True)
    environment = os.environ.copy()
    for filename, key in [("gateway-context.env", "BFF_TESTS_CONTEXT_KEY"), ("bff-redis.env", "BFF_REDIS_PASSWORD")]:
        target = BASE / "local" / filename
        info = target.lstat()
        if target.is_symlink() or not target.is_file() or info.st_uid != os.getuid() or info.st_mode & 0o077:
            raise RuntimeError("Unsafe local BFF credential file")
        match = re.fullmatch(key + r"=([0-9a-f]{64})\n", target.read_text())
        if not match:
            raise RuntimeError("Invalid local BFF credential format")
        environment[key] = match.group(1)
    environment["BFF_REDIS_ADDR"] = "127.0.0.1:6379"
    go = Path.home() / ".local/bin/go"
    os.chdir(ROOT / "services/bff")
    os.execve(go, [str(go), "run", "./cmd/bff"], environment)


if __name__ == "__main__":
    main()
