#!/usr/bin/env python3
"""Exercise all checks against a running validation image; always dry-run.

Usage: python3 validation/matrix.py URL TOKEN_ENV OUTPUT_JSON [FIXTURE_URL]
FIXTURE_URL defaults to URL; use http://127.0.0.1:8080 for local-only testing.
The output contains observations, never tokens or endpoint environment settings.
"""
import json
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path
from urllib.parse import urlsplit

BASE = sys.argv[1].rstrip("/")
TOKEN = dict(line.split("=", 1) for line in Path(sys.argv[2]).read_text().splitlines() if "=" in line)["SALAD_TEST_TOKEN"]
OUTPUT = Path(sys.argv[3])
FIXTURE = (sys.argv[4] if len(sys.argv) > 4 else BASE).rstrip("/")


def request(path, body=None):
    r = urllib.request.Request(BASE + path, data=None if body is None else json.dumps(body).encode(),
                               headers={"Authorization": "Bearer " + TOKEN,
                                        "Content-Type": "application/json", "User-Agent": "salad-preflight-validation/1"})
    # A 409 means the service has not started this run (previous GPU cleanup).
    # Never replay on a timeout, lost response, or any ambiguous status.
    for attempt in range(4):
        try:
            with urllib.request.urlopen(r, timeout=180) as response:
                return json.load(response)
        except urllib.error.HTTPError as error:
            if error.code != 409 or attempt == 3:
                raise
            time.sleep(0.2)


info = request("/info")
results = []
common = {"ATTEMPTS": "3", "FAIL_CONFIRMATIONS": "2", "RETRY_DELAY": "200ms",
          "PROBE_TIMEOUT": "30s", "STARTUP_TIMEOUT": "120s"}


def run(name, env, expected=None, code=0, load=False, action=None):
    report = request("/run", {"id": name, "env": {"SALAD_PREFLIGHT_" + k: v for k, v in (common | env).items()}, "gpu_load": load})
    observed = {e["check"]: e for e in report["events"] if e["event"] == "check_result"}
    errors = []
    if report["exit_code"] != code or report["application_started"] != (code == 0):
        errors.append("application/exit mismatch")
    for check, want in (expected or {}).items():
        if observed.get(check, {}).get("result") != want:
            errors.append(check + " expected " + want)
    decisions = [e for e in report["events"] if e.get("action") == "would_reallocate"]
    want_failure = any(v == "fail" for v in (expected or {}).values())
    if bool(decisions) != want_failure:
        errors.append("dry-run decision mismatch")
    if any(e["event"] == "imds_result" for e in report["events"]):
        errors.append("unexpected IMDS call")
    for e in decisions:
        if not e.get("reason", "").startswith("salad-preflight/v1 code=") or len(e["reason"]) > 1000:
            errors.append("reason contract mismatch")
        if action and e.get("reason_code") != action:
            errors.append("reason code mismatch")
    report["assertion_errors"] = errors
    results.append(report)
    OUTPUT.parent.mkdir(parents=True, exist_ok=True)
    OUTPUT.write_text(json.dumps({"info": info, "results": results}, indent=2))
    observations = {k: {x: v.get(x) for x in ("result", "measurement", "detail")} for k, v in observed.items() if v["result"] != "skipped"}
    print(json.dumps({"case": name, "ok": not errors, "observations": observations, "errors": errors}), flush=True)


specs = [
    ("gpu_total", "GPU_TOTAL_MIN_MIB", "1", "1000000000"),
    ("gpu_free", "GPU_FREE_MIN_MIB", "1", "1000000000"),
    ("gpu_power", "GPU_POWER_MIN_WATTS", "1", "1000000000"),
    ("gpu_driver", "GPU_DRIVER_MIN_VERSION", "0.0.1", "999.999.999"),
    ("gpu_cuda", "GPU_CUDA_MIN_VERSION", "0.0.1", "999.999.999"),
    ("gpu_utilization", "GPU_UTILIZATION_MAX_PERCENT", "100", "0.001"),
    ("gpu_temperature", "GPU_TEMPERATURE_MAX_CELSIUS", "150", "0.001"),
    ("latency", "LATENCY_MAX_MS", "10000", "0.000000001"),
    ("download", "DOWNLOAD_MIN_MBPS", "0.000001", "1000000000"),
    ("upload", "UPLOAD_MIN_MBPS", "0.000001", "1000000000"),
    ("benchmark", "BENCHMARK_MIN_SCORE", "0.000001", "1000000000"),
]
host = urlsplit(FIXTURE)
extras = {
    "latency": {"LATENCY_ADDRESS": f"{host.hostname}:{host.port or (443 if host.scheme == 'https' else 80)}", "LATENCY_SAMPLES": "3"},
    "download": {"DOWNLOAD_URL": FIXTURE + "/download", "DOWNLOAD_BYTES": str(info["download_bytes"]), "DOWNLOAD_SHA256": info["download_sha256"], "DOWNLOAD_BEARER_TOKEN": TOKEN},
    "upload": {"UPLOAD_URL": FIXTURE + "/upload", "UPLOAD_BYTES": str(info["download_bytes"]), "UPLOAD_BEARER_TOKEN": TOKEN},
    "benchmark": {"BENCHMARK_COMMAND": "/opt/validation/benchmark.py"},
}
run("all_disabled", {}, {s[0]: "skipped" for s in specs})
for name, key, passing, failing in specs:
    env = extras.get(name, {}) | {key: passing}
    run(name + "_pass", env, {name: "pass"})
    run(name + "_fail", env | {key: failing}, {name: "fail"}, load=name == "gpu_utilization")
    unknown = {"NVIDIA_SMI": "/missing-nvidia-smi"} if name.startswith("gpu_") else {
        "latency": {"LATENCY_ADDRESS": "127.0.0.1:1"},
        "download": {"DOWNLOAD_SHA256": "0" * 64},
        "upload": {"UPLOAD_URL": FIXTURE + "/bad-ack"},
        "benchmark": {"BENCHMARK_COMMAND": "/missing-benchmark"},
    }[name]
    run(name + "_unknown", env | unknown, {name: "unknown"})
run("unknown_block", {"GPU_FREE_MIN_MIB": "1", "NVIDIA_SMI": "/missing-nvidia-smi", "UNKNOWN_POLICY": "block"}, {"gpu_free": "unknown"}, code=23)
run("multiple_failures", {"GPU_TOTAL_MIN_MIB": "1000000000", "GPU_FREE_MIN_MIB": "1000000000"}, {"gpu_total": "fail", "gpu_free": "fail"}, action="multiple_checks_failed")
run("custom_reason", {"GPU_FREE_MIN_MIB": "1000000000", "GPU_FREE_REASON": "Controlled validation: free VRAM requirement"}, {"gpu_free": "fail"}, action="gpu_free_below_minimum")
run("download_html_unknown", extras["download"] | {"DOWNLOAD_MIN_MBPS": "1", "DOWNLOAD_URL": FIXTURE + "/html"}, {"download": "unknown"})
run("benchmark_timeout", extras["benchmark"] | {"BENCHMARK_MIN_SCORE": "1", "BENCHMARK_TIMEOUT": "1ns"}, {"benchmark": "unknown"})
failures = sum(bool(r["assertion_errors"]) for r in results)
print(json.dumps({"cases": len(results), "passed": len(results) - failures, "failed": failures}))
sys.exit(bool(failures))
