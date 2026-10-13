"""Build/start/stop a loopback Exams process using the existing Core :8003."""
import argparse
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / "dev/scripts"))
from export_structure import read_env
from bootstrap import settings


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=["up", "down", "status"])
    args = parser.parse_args()
    settings()
    local = ROOT / "dev/local"
    env = {key: str(value) for key,value in read_env(local / "exams.env").items()}
    if env["MYSQL_DATABASE"] != "cpm_dev" or env["MYSQL_HOST"] != "127.0.0.1" or env["MYSQL_PORT"] != "13306":
        raise RuntimeError("Only fixed loopback cpm_dev is supported")
    for service in ["exams"]:
        binary = local / ("exams-local-" + service)
        pidfile = local / ("exams-local-" + service + ".pid")
        pid = int(pidfile.read_text()) if pidfile.exists() else None
        alive = False
        if pid:
            result = subprocess.run(["ps", "-p", str(pid), "-o", "command="], capture_output=True, text=True)
            alive = result.returncode == 0 and result.stdout.strip() == str(binary)
        if args.action == "status":
            print(service + (" running" if alive else " stopped"))
            continue
        if alive:
            os.kill(pid, signal.SIGTERM)
            # Wait for graceful exit before replacing a binary/listener.
            import time
            for _ in range(50):
                result = subprocess.run(["ps", "-p", str(pid), "-o", "state="], capture_output=True, text=True)
                if result.returncode or "Z" in result.stdout:
                    break
                time.sleep(0.1)
            else:
                raise RuntimeError(service + " did not stop gracefully")
        pidfile.unlink(missing_ok=True)
        if args.action == "down":
            print(service + " stopped")
            continue
        go = shutil.which("go") or str(Path.home() / ".local/bin/go")
        subprocess.run([go, "build", "-trimpath", "-o", str(binary), "./cmd/" + service], cwd=ROOT / "services" / service, check=True)
        with (local / ("exams-local-" + service + ".log")).open("ab") as log:
            child = subprocess.Popen([str(binary)], cwd=ROOT, env={**os.environ, **env}, stdin=subprocess.DEVNULL,
                                     stdout=log, stderr=log, start_new_session=True)
        pidfile.write_text(str(child.pid) + "\n")
        print(service + " started on isolated loopback listener")


if __name__ == "__main__":
    main()
