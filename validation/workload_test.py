#!/usr/bin/env python3
"""Exercise a live GPU classifier without any reallocation/control endpoint."""
import argparse
from concurrent.futures import ThreadPoolExecutor
import hashlib
import json
from pathlib import Path
import statistics
import time
import urllib.error
import urllib.request

parser = argparse.ArgumentParser()
parser.add_argument("base_url")
parser.add_argument("token_file", type=Path)
parser.add_argument("output", type=Path)
parser.add_argument("--requests", type=int, default=48)
parser.add_argument("--concurrency", type=int, default=4)
args = parser.parse_args()
assert 1 <= args.requests <= 100 and 1 <= args.concurrency <= 8
token = dict(line.split("=", 1) for line in args.token_file.read_text().splitlines())["WORKLOAD_API_TOKEN"]
fixture = args.token_file.parent / "dog.jpg"
if not fixture.exists():
    with urllib.request.urlopen("https://raw.githubusercontent.com/pytorch/hub/master/images/dog.jpg", timeout=30) as response:
        fixture.write_bytes(response.read(4 * 1024 * 1024))
payload = fixture.read_bytes()


def request(path, body=None, authorize=True, content_type="image/jpeg"):
    headers = {"User-Agent": "salad-preflight-workload-test/1", "Content-Type": content_type}
    if authorize: headers["Authorization"] = "Bearer " + token
    req = urllib.request.Request(args.base_url.rstrip("/") + path, data=body, headers=headers)
    with urllib.request.urlopen(req, timeout=60) as response:
        return json.load(response)


def rejected(path, status, **kwargs):
    try: request(path, **kwargs)
    except urllib.error.HTTPError as error:
        assert error.code == status, (path, error.code, status)
        return {"path": path, "status": status}
    raise AssertionError("Expected request rejection")


def classify(batch):
    started = time.perf_counter()
    result = request("/classify?batch_size=" + str(batch), payload)
    result["client_ms"] = (time.perf_counter() - started) * 1000
    assert result["device"] == "cuda:0"
    assert result["batch_size"] == batch and result["gpu_ms"] > 0
    assert result["predictions"][0]["label"] == "Samoyed", result["predictions"]
    assert result["predictions"][0]["score"] > 0.5
    return result


report = {"fixture_source": "https://raw.githubusercontent.com/pytorch/hub/master/images/dog.jpg",
          "fixture_sha256": hashlib.sha256(payload).hexdigest()}
report["health_before"] = request("/health", authorize=False)
report["info_before"] = request("/info")
assert report["info_before"]["ready"] and report["info_before"]["device"] == "cuda:0"
report["negative_cases"] = [rejected("/info", 401, authorize=False),
    rejected("/classify", 401, body=payload, authorize=False),
    rejected("/classify", 415, body=b"invalid", content_type="text/plain"),
    rejected("/classify", 422, body=b"invalid"),
    rejected("/classify?batch_size=33", 422, body=payload)]
report["single"] = classify(1)
started = time.perf_counter()
with ThreadPoolExecutor(max_workers=args.concurrency) as executor:
    report["load"] = list(executor.map(classify, [16] * args.requests))
report["load_seconds"] = time.perf_counter() - started
report["health_after"] = request("/health", authorize=False)
report["info_after"] = request("/info")
before, after = report["info_before"], report["info_after"]
assert before["instance_id"] == after["instance_id"] and before["pid"] == after["pid"]
assert after["requests_completed"] - before["requests_completed"] == args.requests + 1
assert after["images_processed"] - before["images_processed"] == args.requests * 16 + 1
report["summary"] = {"requests_passed": args.requests + 1, "images_classified": args.requests * 16 + 1,
    "concurrency": args.concurrency, "batch_size": 16,
    "negative_cases_passed": len(report["negative_cases"]),
    "median_gpu_ms": statistics.median(row["gpu_ms"] for row in report["load"]),
    "median_client_ms": statistics.median(row["client_ms"] for row in report["load"]),
    "same_instance": True, "cuda": True}
args.output.parent.mkdir(parents=True, exist_ok=True)
args.output.write_text(json.dumps(report, indent=2))
print(json.dumps(report["summary"]))
