# Real GPU application integration — 2026-11-12

Deployed a separate image-classification API with the preflight worker as its
actual entrypoint. On Salad, both enforced checks passed before model loading,
then the application served **49 successful CUDA requests / 769 image inferences**.
The group remains running and ready for inspection. The earlier validation group
remains stopped.

## Image and deployment

- [Separate Dockerfile](../examples/resnet-api/Dockerfile) and
  [usage instructions](../examples/resnet-api/README.md).
- Image: `saladtechnologies/salad-preflight-resnet:20261112-v1`.
- Digest: `sha256:ff7c4585b6b8654762e2332baedffe9728d6cdf61ca6036f22431200f4c5eaf0`.
- Organization/project: `salad-benchmarking` / `recipe-staging`.
- Group: `preflight-resnet-20261112` (`56d5b28d-1dd9-4087-8df5-945b8077145e`).
- One RTX 3060 (12 GB), 4 vCPUs, 8192 MB RAM, 20 GiB storage, high priority.
- Gateway: `https://redpepper-coleslaw-jrhzvxaow5sefhes.salad.cloud`.

The image copies the published `20261112-v1` preflight binary into the official
PyTorch 2.10.0 / CUDA 12.8 runtime. Its command is a normal Python FastAPI/Uvicorn
service, with pretrained torchvision ResNet-18 weights baked into the image.
Weights are verified before loading. Startup has no model download and no CPU
fallback. The published image was verified anonymously pullable, so Salad does
not require registry credentials for this group.

```dockerfile
ENTRYPOINT ["/usr/local/bin/salad-preflight", "--"]
CMD ["python", "/opt/workload/app.py"]
```

The deployed settings include:

```env
SALAD_PREFLIGHT_DRY_RUN=false
SALAD_PREFLIGHT_UNKNOWN_POLICY=block
SALAD_PREFLIGHT_GPU_FREE_MIN_PERCENT=95
SALAD_PREFLIGHT_GPU_POWER_MIN_PERCENT=90
SALAD_PREFLIGHT_GPU_POWER_REFERENCE=default
SALAD_PREFLIGHT_FAIL_CONFIRMATIONS=2
```

Startup probes use `/health` with 20 attempts at 15-second intervals. Readiness
is reported only after model loading and CUDA warmup. The group uses restart
policy `never` to keep failures inspectable during this test.

## Startup evidence

The live node measured **11959 / 12288 MiB free (97.3226%)** and a configured
power limit of **170 / 170 W (100% of default)**. Both checks passed on the first
measurement. No reallocation was requested on this healthy node.

Salad logs establish the following order, all on the same instance:

| Event | UTC time |
| --- | --- |
| Free-VRAM check passed | 18:13:39.288744 |
| Power check passed | 18:13:39.342465 |
| Preflight decided to launch | 18:13:39.343049 |
| Python application started as PID 1 | 18:13:39.415881 |
| Model ready on `cuda:0`, PID 1 | 18:13:43.255893 |

Machine: `888d20b5-9401-8153-adcf-f987565ff04f`.
Instance: `ac7dc644-0093-49de-89f5-ec671011a56b`.
The live `/info` identities matched the Salad API before and after inference.
The process remained PID 1 because the wrapper replaces itself with the original
application command after passing preflight.

Creation at 18:03:59 UTC to application readiness took about 9 minutes 44 seconds,
including image preparation, download, and container startup. The Python import,
model load, and warmup portion took about 3.8 seconds. The large CUDA base layer
also made the initial registry publication substantially slower than the small
validation image.

## Request results

The test used the public PyTorch dog example. ResNet-18 returned **Samoyed** as
the highest-scoring label, with approximately **88.47%** confidence.

| Test | Result |
| --- | --- |
| Single-image CUDA inference | Passed |
| 48 requests, concurrency 4, batch size 16 | All passed; 768 image inferences |
| Missing authorization on `/info` | HTTP 401 |
| Missing authorization on `/classify` | HTTP 401 |
| Unsupported content type | HTTP 415 |
| Invalid image data | HTTP 422 |
| Batch size above the limit | HTTP 422 |
| Health and identity after load | Ready; same instance and process |

The concurrent portion took **7.09 seconds**. Median GPU time was **15.85 ms per
batch of 16**; median client request time was **514.24 ms**. Batches repeat the
submitted sample image, and client timings include gateway, network, preprocessing,
and queueing. This is a short integration test, not a sustained performance or
production-capacity benchmark. PyTorch reported approximately 61.94 MiB of
allocated tensor memory after the test; this excludes CUDA context and other
driver allocations.

Before publication, the same image passed local GPU classification and concurrent
requests (9 successful requests / 129 inferences), plus all five negative API
cases. A separate local run with unavailable NVIDIA telemetry and `UNKNOWN_POLICY=block`
returned exit 23: the Python application never started and no IMDS request was
sent. Networking was disabled for that negative run. The local container was
stopped and removed after testing.

This live run proves the healthy-node path with a real application. The earlier
[percentage reallocation test](validation-2026-11-12.md) separately verified real
IMDS reallocations for insufficient VRAM and insufficient default power.

## Credentials and evidence

The account API key stays in the ignored local `.env.salad`. The application uses
a separate generated token in
`artifacts/resnet-workload-20261112/workload.env` (mode 0600). It is injected only
at runtime; no tokens are baked into image configuration. `/health` is public;
inference and diagnostic endpoints require the application bearer token.
Source scanning found no saved credentials or Gitleaks findings.

Sanitized evidence is retained locally under
`artifacts/resnet-workload-20261112/`: `deployment.json`, `group.json`, `logs.json`,
`startup-verified.json`, `live-test.json`, `final-verified.json`, `local-test.json`,
`local-startup.log`, `local-blocked.log`, `image-inspection.json`, and `push.log`.
Raw artifacts and credentials are excluded from Git and Docker build context.
