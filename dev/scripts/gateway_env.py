"""Provision the dedicated local BFF -> Tests context key without printing it."""
import os
from pathlib import Path
import re
import secrets
import stat

BASE = Path(__file__).resolve().parents[1]
TARGET = BASE / "local/gateway-context.env"


def prepare():
    TARGET.parent.mkdir(parents=True, exist_ok=True)
    if TARGET.exists() or TARGET.is_symlink():
        info = TARGET.lstat()
        if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid():
            raise RuntimeError("Invalid local gateway credential file")
        content = TARGET.read_text()
        if not re.fullmatch(r"BFF_TESTS_CONTEXT_KEY=[0-9a-f]{64}\n", content):
            raise RuntimeError("Invalid local gateway credential format")
        TARGET.chmod(0o600)
        print("Local gateway context credential retained.")
        return
    content = "BFF_TESTS_CONTEXT_KEY=" + secrets.token_hex(32) + "\n"
    descriptor = os.open(TARGET, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
    with os.fdopen(descriptor, "w") as output:
        output.write(content)
        output.flush()
        os.fsync(output.fileno())
    print("Local gateway context credential prepared.")


if __name__ == "__main__":
    prepare()
