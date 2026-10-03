#!/usr/bin/env python3
"""Run full acceptance on a host with Go 1.26, Docker Compose and network access."""
import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import subprocess
import sys
import time


ROOT = Path(__file__).resolve().parent.parent


def main():
    run_id = datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%M%SZ") + "-" + secrets.token_hex(4)
    output = ROOT / "docs/reviews/order-processing-hardening/verification" / ("acceptance-" + run_id)
    output.mkdir(parents=True)
    summary = {"run_id": run_id, "status": "running", "checks": [], "cleanup": "not needed"}
    env = os.environ.copy()
    # Keep this run independent of application configuration and user test databases.
    for key in (
        "DATABASE_URL", "TEST_DATABASE_URL", "CURRENCY", "STORE_REGION", "QUOTE_TTL",
        "JWT_SECRET", "JWT_ISSUER", "JWT_TOKEN_TTL", "HTTP_ADDRESS",
        "PROCESSING_INTERVAL", "PROCESSING_BATCH_SIZE", "AUTH_LOGIN_LIMIT",
        "AUTH_REGISTER_LIMIT", "AUTH_RATE_WINDOW", "AUTH_RATE_MAX_KEYS", "AUTH_MAX_IN_FLIGHT",
        "POSTGRES_PASSWORD", "ADMIN_PASSWORD", "COMPOSE_FILE", "COMPOSE_PROJECT_NAME",
    ):
        env.pop(key, None)
    env["GOMODCACHE"] = str(ROOT / ".cache/gomod")
    env["GOCACHE"] = str(ROOT / ".cache/gobuild")
    # Restore normal downloaded-module verification if an earlier offline run disabled it.
    env["GOPROXY"] = os.environ.get("ACCEPTANCE_GOPROXY", "https://proxy.golang.org,direct")
    env["GOSUMDB"] = os.environ.get("ACCEPTANCE_GOSUMDB", "sum.golang.org")
    password = secrets.token_hex(24)
    container = "orders-acceptance-" + secrets.token_hex(8)
    container_attempted = False
    smoke_attempted = False
    smoke_project = container + "-smoke"

    def scrub(value):
        value = value.replace(password, "<test-password>")
        return re.sub(r"eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+", "<test-token>", value)

    def save():
        (output / "summary.json").write_text(json.dumps(summary, indent=2) + "\n")

    def run(name, args, timeout=900, required=True):
        print(name + " ...", flush=True)
        started = time.monotonic()
        try:
            result = subprocess.run(args, cwd=ROOT, env=env, stdout=subprocess.PIPE,
                                    stderr=subprocess.STDOUT, text=True, timeout=timeout)
            code, text = result.returncode, result.stdout
        except subprocess.TimeoutExpired as error:
            code = 124
            text = error.stdout or ""
            if isinstance(text, bytes):
                text = text.decode(errors="replace")
            text += "\nCommand timed out.\n"
        except OSError as error:
            code, text = 127, str(error)
        (output / (name + ".log")).write_text(
            "Command: " + scrub(json.dumps([str(arg) for arg in args])) +
            "\nExit: " + str(code) + "\n" + scrub(text))
        summary["checks"].append({"name": name, "exit_code": code,
                                  "seconds": round(time.monotonic() - started, 3)})
        save()
        print(name + ": " + ("PASS" if code == 0 else "FAIL"), flush=True)
        if code and required:
            raise RuntimeError(name + " failed; see " + str(output / (name + ".log")))
        return code, text

    def manifests(label):
        hashes = {}
        for filename in ("go.mod", "go.sum"):
            data = (ROOT / filename).read_bytes()
            (output / (filename + "." + label)).write_bytes(data)
            hashes[filename] = hashlib.sha256(data).hexdigest()
        summary["manifests_" + label] = hashes
        save()

    try:
        prerequisites = [run("go-version", ["go", "version"], 30, False)[0],
                         run("docker-access", ["docker", "info", "--format", "{{.ServerVersion}}"], 30, False)[0],
                         run("compose-version", ["docker", "compose", "version"], 30, False)[0]]
        if any(prerequisites):
            summary["status"] = "blocked"
            raise RuntimeError("Execution prerequisites unavailable; acceptance did not run")
        manifests("before")
        run("dependency-tidy", ["go", "mod", "tidy"])
        manifests("after")
        run("module-verification", ["go", "mod", "verify"])
        run("format", ["make", "fmt-check"])
        binary = output / "orders"
        run("build", ["go", "build", "-mod=readonly", "-o", str(binary), "./cmd/orders"])
        run("vet", ["go", "vet", "-mod=readonly", "./..."])
        run("race", ["go", "test", "-mod=readonly", "-count=1", "-race", "./..."])

        # A private ephemeral container and dynamically allocated loopback port.
        # Only this exact generated container is ever removed.
        env["POSTGRES_PASSWORD"] = password
        container_attempted = True
        run("postgres-start", ["docker", "run", "-d", "--name", container,
                               "-e", "POSTGRES_PASSWORD", "-e", "POSTGRES_DB=acceptance",
                               "-p", "127.0.0.1::5432", "postgres:18-alpine"])
        deadline = time.monotonic() + 90
        while True:
            ready = subprocess.run(["docker", "exec", container, "pg_isready", "-U", "postgres", "-d", "acceptance"],
                                   env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=10)
            if ready.returncode == 0:
                break
            if time.monotonic() >= deadline:
                raise RuntimeError("Disposable PostgreSQL did not become ready")
            time.sleep(1)
        _, binding = run("postgres-port", ["docker", "port", container, "5432/tcp"], 30)
        address = binding.strip().splitlines()[0]
        if not address.startswith("127.0.0.1:") or not address.rsplit(":", 1)[1].isdigit():
            raise RuntimeError("Unexpected PostgreSQL loopback port binding")
        database_url = "postgres://postgres:" + password + "@" + address + "/acceptance?sslmode=disable"
        env["DATABASE_URL"] = database_url
        env["TEST_DATABASE_URL"] = database_url
        run("migration-up", [str(binary), "migrate", "up"])
        for version in (3, 2, 1):
            run("migration-down-" + str(version), [str(binary), "migrate", "down"])
        run("migration-up-again", [str(binary), "migrate", "up"])
        run("integration", ["go", "test", "-mod=readonly", "-count=1", "-race", "-v", "-tags=integration", "./..."])
        # The smoke script owns and cleans a separate Compose project/volume.
        # Remove DB settings so it cannot accidentally target this or a user's DB.
        env.pop("DATABASE_URL", None)
        env.pop("TEST_DATABASE_URL", None)
        env["SMOKE_PROJECT_NAME"] = smoke_project
        env["JWT_SECRET"] = password  # Allows Compose interpolation during fallback cleanup.
        smoke_attempted = True
        run("docker-smoke", ["sh", "scripts/smoke-docker.sh"])
        summary["status"] = "passed"
    except (RuntimeError, subprocess.TimeoutExpired, KeyboardInterrupt) as error:
        if summary["status"] != "blocked":
            summary["status"] = "failed"
        summary["error"] = scrub(str(error))
        print(summary["error"], file=sys.stderr)
    finally:
        if smoke_attempted:
            code, _ = run("smoke-cleanup", ["docker", "compose", "-p", smoke_project,
                                           "down", "--volumes"], 120, False)
            if code:
                summary["cleanup"] = "failed"
                summary["status"] = "failed"
        if container_attempted:
            code, _ = run("postgres-cleanup", ["docker", "rm", "-f", "-v", container], 60, False)
            if summary["cleanup"] != "failed":
                summary["cleanup"] = "passed" if code == 0 else "failed"
            if code and summary["status"] == "passed":
                summary["status"] = "failed"
        # Binaries are reproducible and large; evidence retains only text/manifests.
        binary = output / "orders"
        if binary.exists():
            binary.unlink()
        save()
    print("Acceptance " + summary["status"] + ". Evidence: " + str(output), flush=True)
    return 0 if summary["status"] == "passed" else 1


if __name__ == "__main__":
    sys.exit(main())
