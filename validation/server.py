"""Opt-in disposable validation service, never included in the artifact image.

Only /health is public. Other endpoints require SALAD_TEST_TOKEN. A run invokes
the real wrapper with env settings and a harmless application marker. No test
runs automatically, including after restart or reallocation.
"""
import hashlib
import hmac
import json
import os
import socket
import selectors
import subprocess
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

TOKEN = os.environ.get("SALAD_TEST_TOKEN", "")
if len(TOKEN) < 32:
    raise SystemExit("SALAD_TEST_TOKEN must contain at least 32 characters")
PAYLOAD = bytes(range(256)) * 16384  # fixed 4 MiB, never application data
LOCK = threading.Lock()
RESULTS = []
ENFORCED = False
IDENTITY = {k: os.environ.get(k) for k in
            ("SALAD_MACHINE_ID", "SALAD_INSTANCE_ID", "SALAD_CONTAINER_GROUP_ID")}


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass  # do not log authorization headers or request bodies

    def reply(self, code, value, content_type="application/json"):
        data = json.dumps(value).encode() if content_type == "application/json" else value
        self.send_response(code)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def authorized(self):
        if hmac.compare_digest(self.headers.get("Authorization", ""), "Bearer " + TOKEN):
            return True
        self.reply(401, {"error": "unauthorized"})
        return False

    def do_GET(self):
        if self.path == "/health":
            return self.reply(200, {"ready": True})
        if not self.authorized():
            return
        if self.path == "/info":
            return self.reply(200, {"identity": IDENTITY, "download_bytes": len(PAYLOAD),
                                    "download_sha256": hashlib.sha256(PAYLOAD).hexdigest()})
        if self.path == "/download":
            return self.reply(200, PAYLOAD, "application/octet-stream")
        if self.path == "/html":
            return self.reply(200, b"<html>not a measurement</html>", "text/html")
        if self.path == "/results":
            return self.reply(200, RESULTS)
        if self.path == "/diagnostics":
            # Header only: no process table, environment, tokens or command API.
            result = subprocess.run(["nvidia-smi", "--id=0"], capture_output=True,
                                    text=True, timeout=10)
            telemetry = subprocess.run(["nvidia-smi", "--id=0",
                "--query-gpu=memory.total,memory.free,power.limit,power.default_limit,power.max_limit",
                "--format=csv,noheader,nounits"], capture_output=True, text=True, timeout=10)
            return self.reply(200, {"exit_code": result.returncode,
                                    "summary_header": result.stdout.splitlines()[:8],
                                    "telemetry_fields": ["memory.total", "memory.free", "power.limit", "power.default_limit", "power.max_limit"],
                                    "telemetry_exit_code": telemetry.returncode,
                                    "telemetry": telemetry.stdout[:1024].strip()})
        self.reply(404, {"error": "not_found"})

    def do_POST(self):
        global ENFORCED
        if not self.authorized():
            return
        try:
            size = int(self.headers.get("Content-Length", "-1"))
        except ValueError:
            size = -1
        if size < 0 or size > 8 * 1024 * 1024:
            return self.reply(413, {"error": "invalid_size"})
        self.connection.settimeout(180)
        if self.path in ("/upload", "/bad-ack"):
            received = 0
            while received < size:
                chunk = self.rfile.read(min(65536, size - received))
                if not chunk:
                    return self.reply(400, {"error": "short_body"})
                received += len(chunk)
            return self.reply(200, {"received_bytes": received if self.path == "/upload" else 0})
        if self.path != "/run" or size > 32768:
            return self.reply(400, {"error": "invalid_request"})
        try:
            request = json.loads(self.rfile.read(size))
            env = request["env"]
            if not isinstance(env, dict) or any(not k.startswith("SALAD_PREFLIGHT_") or not isinstance(v, str) for k, v in env.items()):
                raise ValueError()
            enforce = request.get("enforce", False)
            if not isinstance(enforce, bool):
                raise ValueError()
        except (ValueError, KeyError, TypeError, AttributeError):
            return self.reply(400, {"error": "invalid_request"})
        if not LOCK.acquire(blocking=False):
            return self.reply(409, {"error": "run_in_progress"})
        load = None
        reply_code = 200
        try:
            if enforce and ENFORCED:
                return self.reply(409, {"error": "enforcement_already_attempted"})
            if enforce:
                ENFORCED = True
            child_env = {k: v for k, v in os.environ.items()
                         if not k.startswith("SALAD_PREFLIGHT_") and k != "SALAD_TEST_TOKEN"}
            child_env.update(env)
            child_env["SALAD_PREFLIGHT_DRY_RUN"] = "false" if enforce else "true"
            if request.get("vram_hold"):
                load = subprocess.Popen(["python3", "/opt/validation/vram-hold.py"],
                                        stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True)
                with selectors.DefaultSelector() as selector:
                    selector.register(load.stdout, selectors.EVENT_READ)
                    if not selector.select(timeout=10) or load.stdout.readline().strip() != "VRAM_HELD":
                        return self.reply(422, {"error": "vram_allocation_failed"})
                time.sleep(1)  # let NVIDIA telemetry reflect the allocation
            if request.get("gpu_load"):
                if load is not None:
                    return self.reply(400, {"error": "choose_one_gpu_fixture"})
                load = subprocess.Popen(["python3", "/opt/validation/gpu-load.py"],
                                        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
                time.sleep(2)
            start = time.monotonic()
            result = subprocess.run(["salad-preflight", "--", "/bin/echo", "PREFLIGHT_APP_STARTED"],
                                    env=child_env, capture_output=True, text=True, timeout=150)
            events = []
            for line in result.stderr.splitlines():
                try:
                    event = json.loads(line)
                except ValueError:
                    continue
                events.append(event)
                print(json.dumps(event), flush=True)
            report = {"id": str(request.get("id", ""))[:100], "identity": IDENTITY,
                      "exit_code": result.returncode, "events": events,
                      "application_started": result.stdout.strip() == "PREFLIGHT_APP_STARTED",
                      "seconds": round(time.monotonic() - start, 3),
                      "vram_hold_running": bool(request.get("vram_hold")) and load is not None and load.poll() is None,
                      "gpu_load_running": load is not None and load.poll() is None}
            RESULTS.append(report)
        except subprocess.TimeoutExpired:
            reply_code, report = 504, {"error": "wrapper_timeout"}
        finally:
            if load is not None and load.poll() is None:
                load.terminate()
                load.wait(timeout=5)
            LOCK.release()
        self.reply(reply_code, report)


class DualStackServer(ThreadingHTTPServer):
    address_family = socket.AF_INET6

    def server_bind(self):
        self.socket.setsockopt(socket.IPPROTO_IPV6, socket.IPV6_V6ONLY, 0)
        super().server_bind()


print(json.dumps({"event": "validation_ready", "port": 8080}), flush=True)
DualStackServer(("::", 8080), Handler).serve_forever()
