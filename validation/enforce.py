#!/usr/bin/env python3
"""One explicit live reallocation, guarded by a local exclusive attempt marker.

Inspect cloud state after ANY outcome. Never remove the marker to blindly retry.
"""
import json
import sys
import urllib.request
from datetime import datetime, timezone

import cloud

if sys.argv[1:] != ["--confirm-live-reallocation"]:
    raise SystemExit("Requires --confirm-live-reallocation on the disposable group")
group = cloud.status()
if group["container"]["image"] != cloud.IMAGE or group["replicas"] != 1:
    raise SystemExit("Unexpected target image or replica count")
base = "https://" + group["networking"]["dns"]
token = dict(line.split("=", 1) for line in (cloud.OUT / "test.env").read_text().splitlines())["SALAD_TEST_TOKEN"]


def request(path, body=None):
    r = urllib.request.Request(base + path, data=None if body is None else json.dumps(body).encode(),
                               headers={"Authorization": "Bearer " + token,
                                        "Content-Type": "application/json", "User-Agent": "salad-preflight-validation/1"})
    with urllib.request.urlopen(r, timeout=90) as response:
        return json.load(response)


before = request("/info")
cloud.save("before-enforcement", before)
env = {"SALAD_PREFLIGHT_GPU_FREE_MIN_MIB": "1000000000",
       "SALAD_PREFLIGHT_GPU_FREE_REASON": "Controlled preflight validation; deliberately impossible threshold",
       "SALAD_PREFLIGHT_ATTEMPTS": "3", "SALAD_PREFLIGHT_FAIL_CONFIRMATIONS": "2",
       "SALAD_PREFLIGHT_RETRY_DELAY": "200ms", "SALAD_PREFLIGHT_STARTUP_TIMEOUT": "30s"}
# First confirm this exact node produces a valid failure. Unknown must stop here.
dry = request("/run", {"id": "pre_enforcement", "env": env})
cloud.save("pre-enforcement", dry)
if not any(e.get("action") == "would_reallocate" and e.get("reason_code") == "gpu_free_below_minimum" for e in dry["events"]):
    raise SystemExit("No confirmed free-VRAM failure; will not enforce")
with (cloud.OUT / "enforcement-attempt.json").open("x") as marker:
    json.dump({"time": datetime.now(timezone.utc).isoformat(), "group": cloud.GROUP, "identity": before["identity"]}, marker)
result = request("/run", {"id": "live_enforcement", "env": env, "enforce": True})
cloud.save("enforcement", result)
print(json.dumps(result))
if result["exit_code"] != 20 or result["application_started"] or not any(e.get("imds", {}).get("outcome") == "accepted" for e in result["events"]):
    raise SystemExit("Reallocation not confirmed accepted; inspect state without repeating")
