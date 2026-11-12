"""A small CUDA image-classification API launched only after startup preflight."""
import asyncio
from contextlib import asynccontextmanager
import hashlib
import io
import json
import os
from pathlib import Path
import secrets
import socket
import time
import uuid
import warnings


def log(event, **fields):
    print(json.dumps({"event": event, "time": time.time(), "pid": os.getpid(),
        "machine_id": os.getenv("SALAD_MACHINE_ID"),
        "instance_id": os.getenv("SALAD_INSTANCE_ID"), **fields}), flush=True)


log("workload_starting")

import torch
from torchvision.models import ResNet18_Weights, resnet18
from PIL import Image, UnidentifiedImageError
from fastapi import FastAPI, Header, HTTPException, Query, Request
from starlette.concurrency import run_in_threadpool
import uvicorn

Image.MAX_IMAGE_PIXELS = 16_000_000
warnings.simplefilter("error", Image.DecompressionBombWarning)
TOKEN = os.environ.get("WORKLOAD_API_TOKEN", "")
if len(TOKEN) < 32:
    raise RuntimeError("WORKLOAD_API_TOKEN must contain at least 32 characters")

STATE = {"ready": False, "requests_completed": 0, "images_processed": 0}
GPU_LOCK = asyncio.Semaphore(1)
MODEL = None
TRANSFORM = ResNet18_Weights.IMAGENET1K_V1.transforms()
METADATA = json.loads((Path(__file__).parent / "model/metadata.json").read_text())


@asynccontextmanager
async def lifespan(app):
    global MODEL
    started = time.perf_counter()
    if not torch.cuda.is_available():
        raise RuntimeError("CUDA is required; CPU fallback is disabled")
    torch.set_num_threads(2)
    path = Path(__file__).parent / "model/resnet18.pt"
    if hashlib.sha256(path.read_bytes()).hexdigest() != METADATA["sha256"]:
        raise RuntimeError("Model checksum mismatch")
    MODEL = resnet18(weights=None)
    MODEL.load_state_dict(torch.load(path, map_location="cpu", weights_only=True))
    MODEL = MODEL.to("cuda:0").eval()
    with torch.inference_mode():
        MODEL(torch.zeros(1, 3, 224, 224, device="cuda:0"))
    torch.cuda.synchronize()
    STATE.update(ready=True, startup_seconds=time.perf_counter() - started)
    log("workload_ready", gpu=torch.cuda.get_device_name(0),
        device=str(next(MODEL.parameters()).device), **STATE)
    yield
    STATE["ready"] = False
    log("workload_stopping")


app = FastAPI(title="Preflight + ResNet-18", lifespan=lifespan,
              docs_url=None, redoc_url=None, openapi_url=None)


def authorize(value):
    if not value or not secrets.compare_digest(value, "Bearer " + TOKEN):
        raise HTTPException(status_code=401, detail="Unauthorized")


@app.get("/health")
async def health():
    if not STATE["ready"]:
        raise HTTPException(status_code=503, detail="Starting")
    return {"status": "ready"}


@app.get("/info")
async def info(authorization: str | None = Header(default=None)):
    authorize(authorization)
    return {**STATE, "model": METADATA["model"], "weights": METADATA["weights"],
        "weights_sha256": METADATA["sha256"], "torch": torch.__version__,
        "cuda_runtime": torch.version.cuda, "gpu": torch.cuda.get_device_name(0),
        "device": str(next(MODEL.parameters()).device), "pid": os.getpid(),
        "gpu_memory_allocated_mib": torch.cuda.memory_allocated() / 2**20,
        "machine_id": os.getenv("SALAD_MACHINE_ID"), "instance_id": os.getenv("SALAD_INSTANCE_ID")}


def infer(payload, batch_size):
    started = time.perf_counter()
    try:
        with Image.open(io.BytesIO(payload)) as image:
            if image.format not in ("JPEG", "PNG"):
                raise ValueError("Unsupported image format")
            tensor = TRANSFORM(image.convert("RGB")).unsqueeze(0)
    except (UnidentifiedImageError, OSError, ValueError, Image.DecompressionBombError,
            Image.DecompressionBombWarning) as error:
        raise HTTPException(status_code=422, detail="Invalid JPEG/PNG image") from error
    tensor = tensor.repeat(batch_size, 1, 1, 1).to("cuda:0")
    start, end = torch.cuda.Event(enable_timing=True), torch.cuda.Event(enable_timing=True)
    with torch.inference_mode():
        start.record()
        probabilities = MODEL(tensor).softmax(dim=1)
        end.record()
    end.synchronize()
    scores, indices = probabilities[0].topk(5)
    predictions = [{"label": METADATA["categories"][index], "score": score}
                   for score, index in zip(scores.tolist(), indices.tolist())]
    result = {"request_id": str(uuid.uuid4()), "batch_size": batch_size,
        "predictions": predictions, "device": str(tensor.device),
        "gpu_ms": start.elapsed_time(end), "total_ms": (time.perf_counter() - started) * 1000}
    STATE["requests_completed"] += 1
    STATE["images_processed"] += batch_size
    log("inference_completed", request_id=result["request_id"], batch_size=batch_size,
        gpu_ms=result["gpu_ms"], total_ms=result["total_ms"])
    return result


@app.post("/classify")
async def classify(request: Request, batch_size: int = Query(default=1, ge=1, le=32),
                   authorization: str | None = Header(default=None)):
    authorize(authorization)
    if request.headers.get("content-type", "").split(";", 1)[0] not in ("image/jpeg", "image/png"):
        raise HTTPException(status_code=415, detail="Send image/jpeg or image/png")
    body = bytearray()
    try:
        async with asyncio.timeout(15):
            async for chunk in request.stream():
                body.extend(chunk)
                if len(body) > 4 * 1024 * 1024:
                    raise HTTPException(status_code=413, detail="Image exceeds 4 MiB")
    except TimeoutError:
        raise HTTPException(status_code=408, detail="Upload timed out") from None
    async with GPU_LOCK:
        return await run_in_threadpool(infer, bytes(body), batch_size)


if __name__ == "__main__":
    # Explicit dual-stack binding works with Salad's gateway and local IPv4 tests.
    sock = socket.socket(socket.AF_INET6, socket.SOCK_STREAM)
    sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    sock.setsockopt(socket.IPPROTO_IPV6, socket.IPV6_V6ONLY, 0)
    sock.bind(("::", 8080))
    config = uvicorn.Config(app, log_level="warning", access_log=False,
        limit_concurrency=16, timeout_keep_alive=5, timeout_graceful_shutdown=20)
    uvicorn.Server(config).run(sockets=[sock])
