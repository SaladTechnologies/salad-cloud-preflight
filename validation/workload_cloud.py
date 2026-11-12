#!/usr/bin/env python3
"""Explicit deployment/status/log commands for the separate ResNet workload."""
import json
import os
from pathlib import Path
import re
import secrets
import sys
import urllib.error
import urllib.request
from datetime import datetime, timezone

from cloud import CONFIG, ROOT

OUT = ROOT / "artifacts/resnet-workload-20261112"
OUT.mkdir(exist_ok=True)
GROUP = "preflight-resnet-20261112"
ORG = "/organizations/" + CONFIG["SALAD_ORGANIZATION"]
COLLECTION = ORG + "/projects/" + CONFIG["SALAD_PROJECT"] + "/containers"
PATH = COLLECTION + "/" + GROUP


def api(path, method="GET", body=None):
    req = urllib.request.Request("https://api.salad.com/api/public" + path, method=method,
        data=None if body is None else json.dumps(body).encode(),
        headers={"Salad-Api-Key": CONFIG["SALAD_API_KEY"], "User-Agent": "salad-preflight-workload/1",
                 "Content-Type": "application/json", "Accept": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=45) as response:
            data = response.read()
            return json.loads(data) if data else {"http_status": response.status}
    except urllib.error.HTTPError as error:
        # An API validation error can echo an injected token. Scrub before saving.
        detail = error.read().decode(errors="replace")
        sensitive = [value for key, value in CONFIG.items()
                     if re.search("KEY|TOKEN|SECRET|PASSWORD", key) and value]
        if (OUT / "workload.env").exists(): sensitive.append(token())
        for secret in sensitive: detail = detail.replace(secret, "<redacted>")
        save("api-error-" + method + "-" + str(error.code),
             {"path": path, "status": error.code, "detail": detail[:10000]})
        raise


def save(name, data):
    (OUT / (name + ".json")).write_text(json.dumps(data, indent=2))


def image():
    value = (OUT / "image.txt").read_text().strip()
    if not re.fullmatch(r"saladtechnologies/salad-preflight-resnet@sha256:[a-f0-9]{64}", value):
        raise SystemExit("Expected the published ResNet workload image pinned by digest")
    return value


def token():
    return dict(line.split("=", 1) for line in (OUT / "workload.env").read_text().splitlines())["WORKLOAD_API_TOKEN"]


def initialize():
    path = OUT / "workload.env"
    if not path.exists():
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, "w") as output:
            output.write("WORKLOAD_API_TOKEN=" + secrets.token_urlsafe(32) + "\n")
    print("Workload token is stored in", path, "(value not displayed)")


def status():
    group = api(PATH)
    safe = json.loads(json.dumps(group))
    safe["container"].pop("registry_authentication", None)
    if "WORKLOAD_API_TOKEN" in safe["container"].get("environment_variables", {}):
        safe["container"]["environment_variables"]["WORKLOAD_API_TOKEN"] = "<redacted>"
    save("group", safe)
    instances = api(PATH + "/instances")
    save("instances", instances)
    print(json.dumps({"name": group["name"], "state": group["current_state"],
        "dns": group["networking"]["dns"], "instances": instances}), flush=True)
    return group


def create():
    if (OUT / "create-attempt.json").exists():
        raise SystemExit("Create already attempted; inspect status rather than repeating")
    try:
        api(PATH)
    except urllib.error.HTTPError as error:
        if error.code != 404:
            raise
    else:
        raise SystemExit("Group name already exists; refusing to replace it")
    quota = api(ORG + "/quotas")["container_groups_quotas"]
    if quota["container_replicas_quota"] - quota["container_replicas_used"] < 1:
        raise SystemExit("Insufficient replica quota")
    gpu = next(g for g in api(ORG + "/gpu-classes")["items"] if g["name"] == "RTX 3060 (12 GB)")
    resources = {"cpu": 4, "memory": 8192, "storage_amount": 20 * 1024**3, "gpu_classes": [gpu["id"]]}
    availability = api(ORG + "/availability/sce-gpu-availability", "POST", resources)
    save("availability", availability)
    print(json.dumps({"gpu": gpu, "availability": availability}), flush=True)
    if availability.get("available_gpu_high", 0) < 1:
        raise SystemExit("No confirmed high-priority capacity")
    http = {"path": "/health", "port": 8080, "scheme": "http",
            "headers": [{"name": "User-Agent", "value": "salad-workload-probe"}]}
    body = {"name": GROUP, "display_name": "Preflight and real GPU image classification",
        "replicas": 1, "restart_policy": "never", "autostart_policy": True,
        "container": {"image": image(), "resources": resources, "priority": "high",
            "environment_variables": {
                "WORKLOAD_API_TOKEN": token(),
                "SALAD_PREFLIGHT_DRY_RUN": "false", "SALAD_PREFLIGHT_UNKNOWN_POLICY": "block",
                "SALAD_PREFLIGHT_GPU_FREE_MIN_PERCENT": "95",
                "SALAD_PREFLIGHT_GPU_POWER_MIN_PERCENT": "90", "SALAD_PREFLIGHT_GPU_POWER_REFERENCE": "default",
                "SALAD_PREFLIGHT_GPU_FREE_REASON": "Image classification requires at least 95% free VRAM at startup",
                "SALAD_PREFLIGHT_GPU_POWER_REASON": "Image classification requires at least 90% default GPU power limit",
                "SALAD_PREFLIGHT_ATTEMPTS": "3", "SALAD_PREFLIGHT_FAIL_CONFIRMATIONS": "2",
                "SALAD_PREFLIGHT_STARTUP_TIMEOUT": "120s", "SALAD_PREFLIGHT_PROBE_TIMEOUT": "10s"}},
        "networking": {"protocol": "http", "port": 8080, "auth": False,
            "client_request_timeout": 60000, "server_response_timeout": 60000},
        "startup_probe": {"http": http, "initial_delay_seconds": 5, "period_seconds": 15,
            "timeout_seconds": 3, "success_threshold": 1, "failure_threshold": 20},
        "readiness_probe": {"http": http, "initial_delay_seconds": 0, "period_seconds": 5,
            "timeout_seconds": 3, "success_threshold": 1, "failure_threshold": 3}}
    safe = json.loads(json.dumps(body))
    safe["container"]["environment_variables"]["WORKLOAD_API_TOKEN"] = "<injected from local workload.env>"
    save("deployment", safe)
    with (OUT / "create-attempt.json").open("x") as output:
        json.dump({"time": datetime.now(timezone.utc).isoformat(), "group": GROUP, "image": image()}, output)
    print(json.dumps({"create": api(COLLECTION, "POST", body).get("id")}), flush=True)
    status()


def logs():
    start = json.loads((OUT / "create-attempt.json").read_text())["time"][:19] + "Z"
    data = api(ORG + "/log-entries", "POST", {
        "start_time": start, "end_time": datetime.now(timezone.utc).isoformat(timespec="seconds"),
        "query": f'resource.labels.container_group_name = "{GROUP}" AND '
            '(resource.type != "container" OR json_log.event = "workload_starting" '
            'OR json_log.event = "workload_ready" OR json_log.event = "decision" '
            'OR json_log.event = "imds_result" '
            'OR (json_log.event = "check_result" AND json_log.result != "skipped"))',
        "page_size": 100, "sort_order": "asc"})
    save("logs", data)
    for entry in data.get("items", []):
        value = entry.get("json_log", entry.get("text_log"))
        if isinstance(value, dict) and (value.get("event") in ("check_result", "decision", "imds_result", "workload_starting", "workload_ready") or "message" in value):
            if value.get("result") != "skipped":
                print(json.dumps({"time": entry["time"], "log": value}))


if __name__ == "__main__":
    command = sys.argv[1]
    try:
        if command == "init": initialize()
        elif command == "create": create()
        elif command == "status": status()
        elif command == "logs": logs()
        elif command == "stop":
            group = api(PATH)
            if group["container"]["image"] != image() or group["replicas"] != 1:
                raise SystemExit("Unexpected deployment; refusing stop")
            if group["current_state"]["status"] != "stopped":
                print(json.dumps(api(PATH + "/stop", "POST")))
            status()
        else: raise SystemExit("Use init, create, status, logs, or stop")
    except urllib.error.HTTPError as error:
        raise SystemExit(f"Salad API HTTP {error.code}; no mutation was retried") from None
