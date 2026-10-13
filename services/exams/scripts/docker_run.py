"""Operate only Exams in the existing cpm_dev Docker stack."""
import argparse
import os
from pathlib import Path
import shutil
import stat
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / "dev/scripts"))
from bootstrap import settings


def docker():
    return shutil.which("docker") or "/Applications/Docker.app/Contents/Resources/bin/docker"


def compose(*arguments):
    values = settings()
    for key in ("MYSQL_DATABASE", "MYSQL_USER", "MYSQL_PASSWORD", "AUTH_INTERNAL_KEY", "RABBITMQ_DOCKER_URL"):
        if key in os.environ and os.environ[key] != str(values.get(key, "")):
            raise SystemExit("Conflicting shell configuration would override guarded dev settings.")
    dev = ROOT / "dev"
    env = dev / "local/exams-compose.env"
    if not env.exists():
        raise SystemExit("Run ./dev/exams prepare first.")
    command = [docker(), "compose", "--project-directory", str(dev), "--env-file", str(dev / ".env")]
    optional = dev / "local/homework-compose.env"
    if optional.exists():
        command += ["--env-file", str(optional)]
    # Sole Auth provision writer creates this private overlay in a separately
    # coordinated window. Missing overlay leaves the optional Exams lane off.
    operations = dev / "local/import-operation-credentials.env"
    operation_keys = ("AUTH_OPERATIONS_EXAMS_KEY", "EXAMS_OPERATIONS_DELIVERY_KEY")
    if operations.exists():
        if not operations.is_file() or operations.is_symlink() or stat.S_IMODE(operations.stat().st_mode) != 0o600:
            raise SystemExit("Operations overlay must be a regular private 0600 file.")
        supplied = {}
        for line in operations.read_text().splitlines():
            if not line.strip() or line.lstrip().startswith("#"):
                continue
            key, separator, value = line.partition("=")
            if not separator or key in supplied:
                raise SystemExit("Invalid operations overlay.")
            supplied[key] = value
        for key in operation_keys:
            if len(supplied.get(key, "")) < 32:
                raise SystemExit("Missing distinct Exams operations credential.")
            if key in os.environ and os.environ[key] != supplied[key]:
                raise SystemExit("Conflicting shell operations configuration.")
        command += ["--env-file", str(operations)]
    elif any(os.environ.get(key) for key in operation_keys):
        raise SystemExit("Operations credentials require the approved private overlay.")
    command += ["--env-file", str(env), "-f", str(dev / "compose.yaml"), "-f", str(dev / "compose.exams.yaml"), "--profile", "exams"]
    subprocess.run(command + list(arguments), check=True, cwd=ROOT)


def offline_build():
    settings()
    # Reuse a cached non-root Linux runtime by immutable ID without rebuilding
    # or restarting Core. No registry access or Go downloads are required.
    runtime = subprocess.check_output([docker(), "image", "inspect", "cpm-core:dev", "--format", "{{.Id}}"], text=True).strip()
    architecture = subprocess.check_output([docker(), "image", "inspect", runtime, "--format", "{{.Architecture}}"], text=True).strip()
    if architecture not in ("arm64", "amd64"):
        raise SystemExit("Unsupported cached runtime architecture.")
    target = ROOT / "dev/local/exams-offline"
    target.mkdir(mode=0o700, parents=True, exist_ok=True)
    go = shutil.which("go") or str(Path.home() / ".local/bin/go")
    env = dict(os.environ, GOOS="linux", GOARCH=architecture, CGO_ENABLED="0", GOPROXY="off")
    subprocess.run([go, "build", "-trimpath", "-ldflags=-s -w", "-o", str(target / "exams"), "./cmd/exams"], cwd=ROOT / "services/exams", env=env, check=True)
    runtime_tag = "cpm-exams-runtime:" + runtime.removeprefix("sha256:")[:16]
    subprocess.run([docker(), "tag", runtime, runtime_tag], check=True)
    (target / "Dockerfile").write_text(f"FROM {runtime_tag}\nCOPY exams /exams\nUSER 65532:65532\nEXPOSE 8010\nENTRYPOINT [\"/exams\"]\n")
    subprocess.run([docker(), "build", "--pull=false", "-t", "cpm-exams:dev", str(target)], check=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=["up", "start", "down", "status", "logs", "build", "offline-build", "offline-up"])
    action = parser.parse_args().action
    if action in ("offline-build", "offline-up"):
        offline_build()
    if action in ("up", "build"):
        compose("build", "exams")
    if action in ("up", "start", "offline-up"):
        compose("up", "-d", "--no-deps", "exams")
    elif action == "down":
        compose("stop", "exams")
        compose("rm", "-f", "exams")
    elif action == "status":
        compose("ps", "exams")
    elif action == "logs":
        compose("logs", "--tail", "60", "exams")


if __name__ == "__main__":
    main()
