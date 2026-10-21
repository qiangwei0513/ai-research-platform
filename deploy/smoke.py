#!/usr/bin/env python3
"""Verify built images in an isolated six-container stack; never use real keys.

Requires Docker Compose and Python 3.10+. Builds/pulls are explicit prerequisites.
Only this script's randomly named project and volumes are removed.
"""
import argparse
import json
import os
from pathlib import Path
import secrets
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.request

ROOT = Path(__file__).resolve().parent.parent
TERMINAL = {"succeeded", "failed", "cancelled"}


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--platform", default="linux/amd64")
    parser.add_argument("--backend-image", default="ai-research-platform-backend:latest")
    parser.add_argument("--agent-image", default="ai-research-platform-agent-service:latest")
    args = parser.parse_args()
    project = "arp-v7-smoke-" + secrets.token_hex(4)
    checks = []
    started = time.monotonic()
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        port = listener.getsockname()[1]
    base = f"http://127.0.0.1:{port}"
    # Ignore host HTTP proxies for these local requests.
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

    with tempfile.TemporaryDirectory(prefix=project + "-") as directory:
        temp = Path(directory)
        agent_env = temp / "agent.env"
        agent_env.write_text(
            "OPENAI_API_KEY=smoke-no-real-key\n"
            "OPENAI_BASE_URL=http://127.0.0.1:9/v1\n"
            "TAVILY_API_KEY=smoke-no-real-key\nOPENAI_AGENTS_DISABLE_TRACING=1\n"
        )
        env_file = temp / "deploy.env"
        values = {
            "MYSQL_PASSWORD": secrets.token_hex(16),
            "MYSQL_ROOT_PASSWORD": secrets.token_hex(16),
            "REDIS_PASSWORD": secrets.token_hex(16),
            "JWT_SECRET": secrets.token_hex(32),
            "APP_PLATFORM": args.platform, "APP_PORT": str(port),
            "BACKEND_IMAGE": args.backend_image, "AGENT_IMAGE": args.agent_image,
            "AGENT_ENV_FILE": str(agent_env),
            "SMOKE_FIXTURE_DIR": str(ROOT / "deploy/tests"),
            "RATE_LIMIT_PER_MINUTE": "0", "WORKER_CONCURRENCY": "2",
        }
        env_file.write_text("".join(f"{key}={value}\n" for key, value in values.items()))
        agent_env.chmod(0o600)
        env_file.chmod(0o600)
        env = dict(os.environ)
        # Shell environment outranks --env-file, so force the isolated values.
        env.update(values)
        env["COMPOSE_PROFILES"] = ""
        command = [
            "docker", "compose", "-p", project, "--env-file", str(env_file),
            "-f", "compose.yaml", "-f", "deploy/compose.data.yaml",
            "-f", "deploy/compose.existing-proxy.yaml",
            "-f", "deploy/tests/compose.smoke.yaml",
        ]

        def run(*arguments, input=None):
            result = subprocess.run(
                [*command, *arguments], cwd=ROOT, env=env, text=True,
                input=input, capture_output=True, timeout=360,
            )
            if result.returncode:
                # Commands never contain a password/token; do not dump config/env.
                raise RuntimeError(f"Compose {' '.join(arguments[:4])} failed:\n{result.stderr[-3000:]}")
            return result.stdout

        def check(name):
            checks.append(name)
            print(f"PASS {name}", flush=True)

        def http(path, method="GET", body=None, token="", expected=200, headers=None):
            headers = dict(headers or {})
            if token:
                headers["Authorization"] = "Bearer " + token
            if body is not None:
                headers["Content-Type"] = "application/json"
            req = urllib.request.Request(
                base + path, data=None if body is None else json.dumps(body).encode(),
                headers=headers, method=method,
            )
            try:
                response = opener.open(req, timeout=20)
            except urllib.error.HTTPError as error:
                response = error
            with response:
                require(response.status == expected, f"{method} {path}: HTTP {response.status}, expected {expected}")
                raw = response.read().decode()
                return json.loads(raw) if "application/json" in response.headers.get("Content-Type", "") else raw

        def sql(statement, database="research", root=False, input=None):
            password = "MYSQL_ROOT_PASSWORD" if root else "MYSQL_PASSWORD"
            user = "root" if root else "research"
            shell = f'MYSQL_PWD="${password}" exec mysql -u {user} -N -B "$@"'
            arguments = ["exec", "-T", "mysql", "sh", "-c", shell, "_", "-D", database]
            if input is None:
                arguments += ["-e", statement]
            return run(*arguments, input=input).strip()

        def redis(service, *arguments):
            return run("exec", "-T", service, "sh", "-c",
                       'REDISCLI_AUTH="$REDIS_PASSWORD" exec redis-cli --raw "$@"',
                       "_", *arguments).strip()

        def wait_task(task_id, token):
            deadline = time.monotonic() + 45
            while time.monotonic() < deadline:
                task = http("/api/tasks/" + task_id, token=token)
                if task["status"] in TERMINAL:
                    require(task["status"] == "succeeded", f"Task terminated as {task['status']}")
                    return task
                time.sleep(0.3)
            raise RuntimeError("Task did not finish within 45 seconds")

        try:
            run("config", "--quiet")
            run("up", "-d", "--no-build", "--pull", "never", "--wait", "--wait-timeout", "240")
            health = http("/health")
            require(health.get("mysql") == "ok" and health.get("queue") == "ok"
                    and health.get("async_tasks") and health.get("task_events"), "Incomplete health response")
            require("<!doctype html>" in http("/").lower(), "Frontend missing")
            check("six services healthy; frontend and API reachable")

            # Inspect actual running containers, not just Compose declarations.
            ids = run("ps", "-q").split()
            containers = json.loads(subprocess.check_output(["docker", "inspect", *ids], text=True))
            require(len(containers) == 6, "Expected exactly six containers")
            for container in containers:
                service = container["Config"]["Labels"]["com.docker.compose.service"]
                bindings = container["HostConfig"]["PortBindings"] or {}
                if service == "backend":
                    require(bindings == {"8080/tcp": [{"HostIp": "127.0.0.1", "HostPort": str(port)}]},
                            "API must bind only to loopback")
                else:
                    require(not bindings, f"{service} unexpectedly exposes a host port")
            for service in ("backend", "worker", "agent-service"):
                require(run("exec", "-T", service, "id", "-u").strip() == "10001", f"{service} runs as root")
                # Check the shipped application tree, excluding third-party venv.
                require(run("exec", "-T", service, "sh", "-c",
                            "find /app -path /app/.venv -prune -o -type f "
                            "\\( -name '.env*' -o -name '*.env' -o -name 'mock_agent.py' \\) -print").strip() == "",
                        f"{service} contains a runtime env file or test fixture")
            check("non-root images; no packaged env/fixture; only API loopback port exposed")
            require(sql("SELECT COUNT(*) FROM schema_migrations") == "3", "Migrations missing")
            check("fresh database automatically migrated through V6")

            credentials = {"email": "v7-smoke@example.test", "password": secrets.token_hex(16)}
            http("/api/auth/register", "POST", credentials, expected=201)
            token = http("/api/auth/login", "POST", credentials)["access_token"]
            answer = http("/api/research", "POST", {"query": "V7 chat", "mode": "chat"})
            require("V7 container chat OK" in answer["result"], "Synchronous Go -> Python failed")
            created = http("/api/tasks", "POST", {"query": "V7 persistence", "mode": "research"},
                           token=token, expected=202)
            task_id = created["task_id"]
            wait_task(task_id, token)
            report = http(f"/api/tasks/{task_id}/report", token=token)
            require("V7 container report" in report["result"], "Report missing")
            stages = http(f"/api/tasks/{task_id}/runs", token=token)["items"]
            require({item["stage"] for item in stages} >= {"planner", "search", "writer"}, "Workflow stages missing")
            check("registration/login; sync chat; async research; stages and report persisted")
            events = http(f"/api/tasks/{task_id}/events", token=token, headers={"Last-Event-ID": "0"})
            require("event: snapshot" in events and "event: succeeded" in events
                    and '"stage":"search"' in events, "SSE snapshot/progress/terminal replay missing")
            check("authenticated SSE replays snapshot, progress and terminal event")

            # Data backup and real restore into a separate database in this test container.
            dump = run("exec", "-T", "mysql", "sh", "-c",
                       'MYSQL_PWD="$MYSQL_PASSWORD" exec mysqldump -u research '
                       '--single-transaction --no-tablespaces research')
            sql("CREATE DATABASE restore_check", root=True)
            sql("", database="restore_check", root=True, input=dump)
            require(sql(f"SELECT COUNT(*) FROM reports WHERE task_id='{task_id}'",
                        database="restore_check", root=True) == "1", "Backup restore lost report")
            require(sql("SELECT COUNT(*) FROM users", database="restore_check", root=True) == "1",
                    "Backup restore lost user")
            check("mysqldump restored users and reports into a separate database")

            run("stop", "redis")
            require(http("/health")["redis"] == "unavailable", "Cache outage should degrade health detail")
            require(http(f"/api/tasks/{task_id}/report", token=token) == report, "Cache fallback lost data")
            run("start", "redis")
            check("cache Redis outage preserves database reads")

            # Bypass Compose dependencies to exercise the applications' own startup retry.
            run("stop", "mysql")
            http("/health", expected=503)
            http("/ping")
            run("up", "-d", "--no-deps", "--no-build", "--pull", "never", "--force-recreate", "backend", "worker")
            time.sleep(2)
            for service in ("backend", "worker"):
                require("mysql not ready" in run("logs", "--no-color", service), f"{service} retry not observed")
            run("start", "mysql")
            run("up", "-d", "--no-build", "--pull", "never", "--wait", "--wait-timeout", "240")
            for service in ("backend", "worker"):
                container = run("ps", "-q", service).strip()
                restarts = subprocess.check_output(
                    ["docker", "inspect", "-f", "{{.RestartCount}}", container], text=True).strip()
                require(restarts == "0", f"{service} restarted instead of recovering within startup")
            check("MySQL outage returns 503; API/Worker retry and recover without process restart")

            redis("redis-queue", "SET", "v7:volume-proof", "durable")
            redis("redis", "SET", "v7:cache-proof", "temporary")
            before_volumes = set(run("config", "--volumes").split())
            run("up", "-d", "--no-build", "--pull", "never", "--force-recreate", "--wait", "--wait-timeout", "240")
            require(set(run("config", "--volumes").split()) == before_volumes, "Volume definitions changed")
            token = http("/api/auth/login", "POST", credentials)["access_token"]
            require(http(f"/api/tasks/{task_id}/report", token=token) == report, "Recreation changed stored report")
            require(http("/api/tasks", token=token)["total"] >= 1, "Recreation lost task history")
            require(sql(f"SELECT COUNT(*) FROM task_events WHERE task_id='{task_id}'") != "0", "Events lost")
            require(redis("redis-queue", "GET", "v7:volume-proof") == "durable", "Queue AOF volume lost")
            require(redis("redis", "GET", "v7:cache-proof") == "", "Cache unexpectedly persisted")
            check("all containers recreated; accounts/reports/events and queue AOF retained; cache disposable")

            result = {
                "version": "V7", "platform": args.platform, "upstreams": "test substitutes; no paid calls",
                "checks": checks, "duration_seconds": round(time.monotonic() - started, 1),
                "images": {c["Config"]["Labels"]["com.docker.compose.service"]: c["Image"] for c in containers},
            }
            output = ROOT / "test-results/v7-compose.json"
            output.parent.mkdir(exist_ok=True)
            output.write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n")
            print(f"PASS all checks; evidence: {output}", flush=True)
        finally:
            # Project name is generated here, never accepted from the user/env.
            # Production/dev project volumes are outside this scope.
            run("down", "--volumes", "--remove-orphans")
            print(f"Removed isolated test project {project}", flush=True)


if __name__ == "__main__":
    main()
