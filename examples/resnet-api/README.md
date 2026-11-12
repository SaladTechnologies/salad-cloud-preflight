# GPU image classification with startup preflight

This is a runnable application image: a FastAPI service performs image
classification with pretrained ResNet-18 on CUDA. Its Dockerfile copies the
published static preflight binary into an official PyTorch CUDA runtime and
wraps the application's original command:

```dockerfile
ENTRYPOINT ["/usr/local/bin/salad-preflight", "--"]
CMD ["python", "/opt/workload/app.py"]
```

The [live test report](../../docs/resnet-workload-2026-11-12.md) records a passing
Salad startup and 49 successful GPU inference requests.

The preflight runs before Python imports PyTorch or loads the model. On a passing
node it replaces itself with the application process. On a confirmed failure it
requests reallocation and exits without launching the application. The real
application runs automatically on each accepted node; there is no `/run` endpoint
or separate test service inside this image.

## Build

From the repository root:

```sh
docker build --platform linux/amd64 -f examples/resnet-api/Dockerfile \
  -t saladtechnologies/salad-preflight-resnet:20261112-v1 .
```

Both base images are pinned by digest. PyTorch 2.10.0 / torchvision 0.25.0 use
CUDA 12.8. FastAPI and Uvicorn versions are pinned in `requirements.txt`.
`prepare_model.py` downloads the official `IMAGENET1K_V1` weights with hash
checking during the build. Weights and the category list are included in the
image; startup does not download models. The application checks the stored
model checksum again before loading it with `weights_only=True`.

## Runtime

Inject a random `WORKLOAD_API_TOKEN` with at least 32 characters, plus:

```env
SALAD_PREFLIGHT_DRY_RUN=false
SALAD_PREFLIGHT_UNKNOWN_POLICY=block
SALAD_PREFLIGHT_GPU_FREE_MIN_PERCENT=95
SALAD_PREFLIGHT_GPU_POWER_MIN_PERCENT=90
SALAD_PREFLIGHT_GPU_POWER_REFERENCE=default
```

Both checks must pass before model loading. Two consecutive failures are
required. Unknown telemetry blocks startup without requesting reallocation
solely for the unknown result. Power compares the configured limit to the
default limit. These thresholds are a test requirement, not a statement that
ResNet-18 needs 95% of a 12 GB GPU. The checks run once at startup; application
allocations subsequently reduce free VRAM without triggering another check.

The service requires CUDA and has no CPU fallback. It loads the model onto
`cuda:0`, warms it up, and only then becomes healthy. One process serves requests;
GPU inference is serialized while health requests remain responsive. Inputs are
bounded to 4 MiB JPEG/PNG images and 16 million pixels. Inference uses the
torchvision weight's resize, crop, and normalization transform.

| Endpoint | Authentication | Purpose |
| --- | --- | --- |
| `GET /health` | None | Minimal readiness response after model warmup |
| `GET /info` | Bearer token | Model, CUDA device, process identity, request counters |
| `POST /classify` | Bearer token | Raw JPEG/PNG body; returns five labels and scores |

`/classify?batch_size=16` repeats the submitted image into a batch of 16 for
bounded load testing; valid sizes are 1–32. GPU time measures the forward pass
and softmax with CUDA events. Client time also includes transfer, preprocessing,
and queueing. These measurements are not a general inference benchmark.

## Salad deployment and validation

`validation/workload_cloud.py` targets a **new** group,
`preflight-resnet-20261112`, in the organization/project from the local
`.env.salad`. It never edits the earlier validation group. Initialize its
separate application token with:

```sh
python3 validation/workload_cloud.py init
```

The token is saved to the ignored
`artifacts/resnet-workload-20261112/workload.env` with mode 0600. The account API
key stays local. After publishing the image, write its immutable image reference
to `artifacts/resnet-workload-20261112/image.txt`, then:

```sh
python3 validation/workload_cloud.py create
python3 validation/workload_cloud.py status
python3 validation/workload_cloud.py logs
python3 validation/workload_test.py https://GROUP.salad.cloud \
  artifacts/resnet-workload-20261112/workload.env \
  artifacts/resnet-workload-20261112/live-test.json
```

The deployment uses one RTX 3060 (12 GB), 4 vCPUs, 8 GiB RAM, and 20 GiB storage.
Startup probes allow approximately five minutes for preflight and model warmup.
Readiness probes use `/health`. The test group uses restart policy `never` so
application failures remain inspectable. A successful IMDS reallocation still
moves the workload to another node. Probe failures can also cause Salad to
replace a node; inspect system events to distinguish the cause.

The client downloads the public PyTorch dog example into ignored artifacts,
checks that it is classified as Samoyed on CUDA, tests authentication and malformed
requests, then sends 48 requests at concurrency four with batches of 16. This is
49 successful requests / 769 image inferences including the initial single image.
The client checks readiness and that the same application instance remains alive.
It never changes thresholds or calls reallocation.

Creation is recorded before sending the API request. Do not delete the attempt
record or repeat an uncertain create; use read-only status and logs. The helper
has an explicit `stop` command for cleanup when authorized.

Sources: [ResNet-18 weights and transforms](https://docs.pytorch.org/vision/stable/models/generated/torchvision.models.resnet18.html),
[official PyTorch example image](https://pytorch.org/hub/pytorch_vision_resnet/),
[Salad startup probes](https://docs.salad.com/container-engine/explanation/infrastructure-platform/startup-probes).
