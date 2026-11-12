#!/usr/bin/env python3
"""Explicit 95%-VRAM / 90%-power tests, with separate durable attempt records.

No host settings are changed. Power enforcement requires an actual low reading.
"""
import json
import sys
import urllib.request
from datetime import datetime, timezone

import cloud

OUT = cloud.OUT / "percent-20261112"
OUT.mkdir(exist_ok=True)
TOKEN = dict(line.split("=", 1) for line in (cloud.OUT / "test.env").read_text().splitlines())["SALAD_TEST_TOKEN"]
POWER_REFERENCE = "default"
if "--power-max" in sys.argv:
    POWER_REFERENCE = "max"
GROUP = cloud.api(cloud.PATH)
if GROUP["container"]["image"] != cloud.IMAGE or GROUP["replicas"] != 1:
    raise SystemExit("Unexpected validation deployment")
BASE = "https://" + GROUP["networking"]["dns"]


def request(path, body=None):
    req = urllib.request.Request(BASE + path, data=None if body is None else json.dumps(body).encode(),
        headers={"Authorization": "Bearer " + TOKEN, "Content-Type": "application/json",
                 "User-Agent": "salad-preflight-validation/1"})
    with urllib.request.urlopen(req, timeout=90) as response:
        return json.load(response)


def save(name, value):
    (OUT / (name + ".json")).write_text(json.dumps(value, indent=2))


def run(name, check, enforce=False, hold=False):
    env = {"SALAD_PREFLIGHT_ATTEMPTS": "3", "SALAD_PREFLIGHT_FAIL_CONFIRMATIONS": "2",
           "SALAD_PREFLIGHT_RETRY_DELAY": "1s", "SALAD_PREFLIGHT_STARTUP_TIMEOUT": "30s",
           "SALAD_PREFLIGHT_PROBE_TIMEOUT": "5s", "SALAD_PREFLIGHT_UNKNOWN_POLICY": "block"}
    if check == "gpu_free":
        env |= {"SALAD_PREFLIGHT_GPU_FREE_MIN_PERCENT": "95",
                "SALAD_PREFLIGHT_GPU_FREE_REASON": "Validation: at least 95% VRAM free" + ("; temporary test allocation" if hold else "")}
    else:
        env |= {"SALAD_PREFLIGHT_GPU_POWER_MIN_PERCENT": "90",
                "SALAD_PREFLIGHT_GPU_POWER_REFERENCE": POWER_REFERENCE,
                "SALAD_PREFLIGHT_GPU_POWER_REASON": "Validation: configured power limit below 90% of " + POWER_REFERENCE}
    report = request("/run", {"id": name, "env": env, "enforce": enforce, "vram_hold": hold})
    save(name, report)
    print(json.dumps({"case": name, "exit_code": report["exit_code"], "application_started": report["application_started"],
        "identity": report["identity"], "events": [x for x in report["events"] if x.get("check") == check or x["event"] in ("decision", "imds_result")]}), flush=True)
    return report


def observed(report, check):
    return next(e for e in report["events"] if e["event"] == "check_result" and e["check"] == check)


def enforce(scenario, check, hold):
    marker = OUT / (scenario + "-attempt.json")
    if marker.exists():
        raise SystemExit("Attempt already recorded; verify state instead of repeating")
    before = request("/info")
    save(scenario + "-before", before)
    dry = run(scenario + "-dry", check, hold=hold)
    if observed(dry, check)["result"] != "fail":
        raise SystemExit("No confirmed real failure at the requested percentage; no reallocation sent")
    with marker.open("x") as output:
        json.dump({"time": datetime.now(timezone.utc).isoformat(), "identity": before["identity"],
                   "scenario": scenario, "reference": POWER_REFERENCE}, output)
    try:
        result = run(scenario + "-enforced", check, enforce=True, hold=hold)
    except (TimeoutError, OSError, ValueError) as error:
        # Reallocation can destroy the service before its response arrives.
        # Preserve the durable attempt and inspect cloud state; never resend.
        save(scenario + "-response-unavailable", {"error_type": type(error).__name__,
             "outcome": "unknown", "retry_allowed": False})
        raise SystemExit("Enforcement response unavailable; keep the attempt marker and run verify-" + scenario + "; do not repeat enforcement") from None
    if result["application_started"] or result["exit_code"] not in (20, 22):
        raise SystemExit("Unexpected enforcement outcome; inspect state, do not retry")
    print("Request completed; independently verify replacement, including after HTTP 200/uncertain.")


def verify(scenario):
    before = json.loads((OUT / (scenario + "-before.json")).read_text())["identity"]
    instances = cloud.api(cloud.PATH + "/instances")["instances"]
    if len(instances) != 1 or not instances[0].get("ready"):
        raise SystemExit("Replacement not ready; repeat only this read")
    current = request("/info")["identity"]
    assert current["SALAD_MACHINE_ID"] == instances[0]["machine_id"]
    assert current["SALAD_INSTANCE_ID"] == instances[0]["id"]
    assert current["SALAD_MACHINE_ID"] != before["SALAD_MACHINE_ID"]
    assert current["SALAD_INSTANCE_ID"] != before["SALAD_INSTANCE_ID"]
    evidence = {"before": before, "after": current, "ready": True, "machine_changed": True,
                "automatic_runs": len(request("/results"))}
    assert evidence["automatic_runs"] == 0
    save(scenario + "-verified", evidence)
    print(json.dumps(evidence))


command = sys.argv[1]
if command == "observe":
    diag = request("/diagnostics"); save("diagnostics", diag); print(json.dumps(diag), flush=True)
    run("vram-natural", "gpu_free")
    run("power-natural", "gpu_power")
    run("vram-pressure-dry", "gpu_free", hold=True)
elif command == "vram":
    enforce("vram", "gpu_free", "--natural" not in sys.argv)
elif command == "power":
    enforce("power", "gpu_power", False)
elif command in ("verify-vram", "verify-power"):
    verify(command.removeprefix("verify-"))
else:
    raise SystemExit("Use observe, vram, power, verify-vram, or verify-power")
