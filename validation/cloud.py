#!/usr/bin/env python3
"""Explicit operations for the one disposable validation group. No POST retries."""
import json
import sys
import urllib.error
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
OUT = ROOT / "artifacts"
OUT.mkdir(exist_ok=True)
CONFIG = dict(line.split("=", 1) for line in (ROOT / ".env.salad").read_text().splitlines() if "=" in line and not line.startswith("#"))
GROUP = "preflight-validation-20261111"
ORG = "/organizations/" + CONFIG["SALAD_ORGANIZATION"]
COLLECTION = ORG + "/projects/" + CONFIG["SALAD_PROJECT"] + "/containers"
PATH = COLLECTION + "/" + GROUP
IMAGE = "saladtechnologies/salad-preflight-validation@sha256:001b5eae7ce84ef17da79169b4be11138a73ef79ecb8f95e5511d7b338b7c016"


def api(path, method="GET", body=None):
    req = urllib.request.Request("https://api.salad.com/api/public" + path, method=method,
        data=None if body is None else json.dumps(body).encode(),
        headers={"Salad-Api-Key": CONFIG["SALAD_API_KEY"], "User-Agent": "salad-preflight-validation/1",
                 "Content-Type": "application/merge-patch+json" if method == "PATCH" else "application/json", "Accept": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=45) as response:
            data = response.read()
            return json.loads(data) if data else {"http_status": response.status}
    except urllib.error.HTTPError as error:
        # Scrub credentials before displaying API validation errors.
        detail = error.read().decode(errors="replace")
        secrets = [CONFIG.get("SALAD_API_KEY", "")]
        if (OUT / "test.env").exists():
            secrets += [x.split("=", 1)[1] for x in (OUT / "test.env").read_text().splitlines() if "=" in x]
        for secret in secrets:
            if secret:
                detail = detail.replace(secret, "<redacted>")
        print(detail[:2000], file=sys.stderr)
        raise SystemExit(f"Salad API {method} {path}: HTTP {error.code}; no automatic retry")


def save(name, value):
    (OUT / (name + ".json")).write_text(json.dumps(value, indent=2))


def status():
    group = api(PATH)
    safe = {k: v for k, v in group.items() if k != "container"}
    safe["container"] = {k: v for k, v in group["container"].items() if k not in ("environment_variables", "registry_authentication")}
    save("group", safe)
    instances = api(PATH + "/instances")
    save("instances", instances)
    print(json.dumps({"name": group["name"], "state": group["current_state"],
                      "networking": group.get("networking"), "instances": instances}))
    return group


def create():
    existing = api(COLLECTION)["items"]
    if any(g["name"] == GROUP for g in existing):
        raise SystemExit("Exact group name already exists; inspect status, do not recreate")
    quota = api(ORG + "/quotas")["container_groups_quotas"]
    if quota["container_replicas_quota"] - quota["container_replicas_used"] < 1:
        raise SystemExit("Insufficient quota")
    classes = api(ORG + "/gpu-classes")["items"]
    gpu = next(g for g in classes if g["name"] == "RTX 3060 (12 GB)")
    resources = {"cpu": 2, "memory": 2048, "storage_amount": 5 * 1024**3, "gpu_classes": [gpu["id"]]}
    availability = api(ORG + "/availability/sce-gpu-availability", "POST", resources)
    save("availability", availability)
    print(json.dumps({"selected_gpu": gpu, "availability": availability}), flush=True)
    if availability.get("available_gpu_high", 0) < 1:
        raise SystemExit("No confirmed high-priority availability; group not created")
    token = dict(line.split("=", 1) for line in (OUT / "test.env").read_text().splitlines())["SALAD_TEST_TOKEN"]
    body = {"name": GROUP, "display_name": "Startup preflight validation", "replicas": 1,
            "restart_policy": "never", "autostart_policy": True,
            "container": {"image": IMAGE, "resources": resources, "priority": "high",
                          "environment_variables": {"SALAD_TEST_TOKEN": token}},
            "networking": {"protocol": "http", "port": 8080, "auth": False,
                           "client_request_timeout": 100000, "server_response_timeout": 100000},
            "readiness_probe": {"http": {"path": "/health", "port": 8080, "scheme": "http", "headers": [{"name": "User-Agent", "value": "salad-preflight-probe"}]},
                                "initial_delay_seconds": 5, "period_seconds": 5, "timeout_seconds": 2,
                                "success_threshold": 1, "failure_threshold": 3}}
    safe = json.loads(json.dumps(body))
    safe["container"]["environment_variables"] = {"SALAD_TEST_TOKEN": "<injected from artifacts/test.env>"}
    save("deployment", safe)
    api(COLLECTION, "POST", body)
    status()


if __name__ == "__main__":
    command = sys.argv[1]
    if command == "create":
        create()
    elif command == "status":
        status()
    elif command == "update":
        group = api(PATH)
        if not group["container"]["image"].startswith("saladtechnologies/salad-preflight-validation@sha256:") or group["replicas"] != 1:
            raise SystemExit("Group no longer matches validation deployment")
        if group["container"]["image"] != IMAGE:
            api(PATH, "PATCH", {"container": {"image": IMAGE}})
        status()
    elif command == "stop":
        group = api(PATH)
        # Restrict cleanup to this validation image and one test replica.
        if group["container"]["image"] != IMAGE or group["replicas"] != 1:
            raise SystemExit("Group no longer matches validation deployment")
        print(json.dumps(api(PATH + "/stop", "POST")))
        status()
    elif command == "start":
        group = api(PATH)
        if group["container"]["image"] != IMAGE or group["replicas"] != 1 or group["current_state"]["status"] != "stopped" or group.get("pending_change"):
            raise SystemExit("Expected one stopped validation replica with the current image ready")
        quota = api(ORG + "/quotas")["container_groups_quotas"]
        if quota["container_replicas_quota"] - quota["container_replicas_used"] < 1:
            raise SystemExit("Insufficient quota")
        resources = {k: v for k, v in group["container"]["resources"].items() if k in ("cpu", "memory", "storage_amount", "gpu_classes")}
        availability = api(ORG + "/availability/sce-gpu-availability", "POST", resources)
        print(json.dumps(availability))
        if availability.get("available_gpu_high", 0) < 1:
            raise SystemExit("No confirmed high-priority availability")
        print(json.dumps(api(PATH + "/start", "POST")))
        status()
    else:
        raise SystemExit("Use create, update, start, status, or stop")
