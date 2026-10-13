#!/usr/bin/env python3
"""Create local Redis credentials once; never print or overwrite their value."""

import argparse
import os
from pathlib import Path
import re
import secrets
import stat
import tempfile


def prepare(path: Path) -> None:
    if path.is_symlink():
        raise ValueError("Redis credentials must not be a symlink")
    path.parent.mkdir(parents=True, exist_ok=True)
    if not path.exists():
        # Publish a fully written private file atomically. Another invocation
        # winning the race keeps its credentials; neither replaces that file.
        temporary = None
        try:
            with tempfile.NamedTemporaryFile(mode="w", dir=path.parent, prefix=".bff-redis-", delete=False) as handle:
                temporary = Path(handle.name)
                os.fchmod(handle.fileno(), 0o600)
                handle.write("BFF_REDIS_PASSWORD=" + secrets.token_hex(32) + "\n")
                handle.flush()
                os.fsync(handle.fileno())
            try:
                os.link(temporary, path)
            except FileExistsError:
                pass
        finally:
            if temporary is not None:
                temporary.unlink(missing_ok=True)
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_uid != os.geteuid():
        raise ValueError("Redis credentials must be a regular file owned by this user")
    # Revalidate the opened inode and chmod that descriptor. A path replaced
    # between checking and opening cannot redirect permissions to a symlink.
    descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    with os.fdopen(descriptor) as handle:
        info = os.fstat(handle.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_uid != os.geteuid():
            raise ValueError("Redis credentials must be a regular file owned by this user")
        if not re.fullmatch(r"BFF_REDIS_PASSWORD=[a-f0-9]{64}\n", handle.read()):
            raise ValueError("Existing Redis credentials file is invalid; it was not replaced")
        os.fchmod(handle.fileno(), 0o600)


def main() -> None:
    default = Path(__file__).resolve().parents[3] / "dev" / "local" / "bff-redis.env"
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--path", type=Path, default=default)
    args = parser.parse_args()
    try:
        prepare(args.path)
    except (OSError, ValueError):
        # Exception details can contain file contents; keep all secret-related
        # diagnostics generic, including failures from an existing file.
        parser.exit(1, "Could not prepare Redis credentials; existing values were preserved.\n")
    print("BFF Redis credentials prepared.")


if __name__ == "__main__":
    main()
