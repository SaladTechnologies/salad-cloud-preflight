#!/usr/bin/env python3
"""Read-only verification after an accepted OR uncertain live IMDS result."""
import json
import urllib.request

import cloud

before = json.loads((cloud.OUT / "before-enforcement.json").read_text())["identity"]
group = cloud.status()
instances = cloud.api(cloud.PATH + "/instances")["instances"]
if len(instances) != 1 or not instances[0].get("ready"):
    raise SystemExit("Replacement is not yet ready; only repeat this read-only verification")
instance = instances[0]
if instance["machine_id"] == before["SALAD_MACHINE_ID"] or instance["id"] == before["SALAD_INSTANCE_ID"]:
    raise SystemExit("A different ready machine has not been observed")
token = dict(line.split("=", 1) for line in (cloud.OUT / "test.env").read_text().splitlines())["SALAD_TEST_TOKEN"]
base = "https://" + group["networking"]["dns"]


def get(path):
    request = urllib.request.Request(base + path, headers={"Authorization": "Bearer " + token,
                                      "User-Agent": "salad-preflight-validation/1"})
    with urllib.request.urlopen(request, timeout=20) as response:
        return json.load(response)


info, results = get("/info"), get("/results")
assert info["identity"]["SALAD_MACHINE_ID"] == instance["machine_id"]
assert info["identity"]["SALAD_INSTANCE_ID"] == instance["id"]
assert results == [], "Unexpected automatic test run after reallocation"
evidence = {"before": before, "after": info["identity"], "ready": True,
            "machine_changed": True, "instance_changed": True, "automatic_runs": len(results)}
cloud.save("reallocation-verified", evidence)
print(json.dumps(evidence))
