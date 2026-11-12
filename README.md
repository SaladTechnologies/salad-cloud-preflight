# SaladCloud startup preflight

A startup wrapper that checks a node before executing your application. It builds
into one static Go binary, uses no third-party Go modules, and requires no changes
to application code. All checks start disabled; dry-run starts enabled. There is
no background monitoring in this version.

GPU checks read NVIDIA telemetry through `nvidia-smi`. Network checks stream
bounded transfers to endpoints you control and time TCP connections to an address
you choose. A benchmark check runs an executable you supply. Only confirmed
measurements on the wrong side of a configured threshold can request reallocation.
Missing tools, unsupported telemetry, bad responses and timeouts are **unknown**,
never a measured zero.

## Build and integrate

Build with Go 1.23 or newer on Linux (CI uses Go 1.27):

```sh
CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags='-s -w' -o bin/salad-preflight ./cmd/salad-preflight
./bin/salad-preflight -- your-application argument1 'argument with spaces'
```

Cross-build on another OS with `GOOS=linux GOARCH=amd64 CGO_ENABLED=0`. Match
`GOARCH` to the container platform. The wrapper uses Linux `exec`: the application
replaces it with the same PID, working directory, environment, stdin/stdout/stderr,
and normal application signal/exit behavior. No shell parses your arguments. If
shell expansion is needed, explicitly pass `/bin/sh -c '...'`. The image must
contain that shell. Use your normal init (such as tini) if the application needs
child reaping. SIGINT/SIGTERM during preflight cancels startup.

Build an artifact image and copy its binary into a customer image:

```sh
docker build -t salad-preflight:local .
```

```dockerfile
FROM salad-preflight:local AS preflight
FROM your-existing-image:tag
COPY --from=preflight /salad-preflight /usr/local/bin/salad-preflight
ENTRYPOINT ["/usr/local/bin/salad-preflight", "--", "/your/original-entrypoint"]
CMD ["your-original-command", "argument"]
```

Inspect the original image's Entrypoint and Cmd and preserve both. Docker resets
inherited CMD when ENTRYPOINT changes. A container runtime command override can
bypass the wrapper, so configure the deployed command to retain it. Keep application
initialization after the preflight, including any existing entrypoint script.

See [Python](examples/Dockerfile.python), [Node on Alpine](examples/Dockerfile.node-alpine),
[an existing GPU/customer image](examples/Dockerfile.existing), and
[distroless](examples/Dockerfile.distroless) examples. These are integration templates:
provide your app, dependencies, base image, and original commands. They are not
standalone demo applications. Pin your production base images by digest. The root
Dockerfile exports only a binary; it has no application or default command.

For a runnable GPU application, see the [ResNet image-classification API](examples/resnet-api/README.md).
Its separate Dockerfile copies the published worker into a CUDA application image.
The startup checks run before model loading, then the application serves real
image inference requests. Deployment and load-test scripts target a separate Salad group.

If the container group has a SaladCloud
[startup probe](https://docs.salad.com/container-engine/explanation/infrastructure-platform/startup-probes),
the preflight runs inside that probe's window, because the application has not
started yet. Size the probe for `STARTUP_TIMEOUT` plus your application's own
startup, or the instance is interrupted for a startup probe failure.

Copy `.env.example` to `.env` and inject it with `docker run --env-file .env ...`,
Compose `env_file`, or SaladCloud container environment settings. The binary does
not load `.env` files. Keep tokens out of image layers and source control.

## Decisions and bounded recovery

Checks run sequentially: total VRAM, free VRAM, power limit, driver version, CUDA
version, GPU utilization, GPU temperature, latency, download, upload, benchmark.
Each enabled check has up to `ATTEMPTS` measurements. Minimum checks (`_MIN_`
keys) pass at or above the threshold; maximum checks (`_MAX_` keys) pass at or
below it. One passing value immediately passes the check. A failure requires
`FAIL_CONFIRMATIONS` consecutive valid measurements on the wrong side of the
threshold (default two). An unknown resets that streak. A failing value followed
by a pass passes; an incomplete streak at exhaustion is unknown. Retries wait
`RETRY_DELAY`, including after failing values. Disabled checks are skipped without
executing tools or sending requests.

`PROBE_TIMEOUT` bounds each attempt, including response streaming; the benchmark
uses `BENCHMARK_TIMEOUT` instead. The single
`STARTUP_TIMEOUT` bounds all probes, retry waits, and IMDS together. The subprocess
cleanup grace can add up to 100ms; OS scheduling can add small overhead. Budget
exhaustion marks unfinished checks unknown. Select a budget that fits your enabled
checks: five checks × three attempts × 15s exceeds the default 120s. There is no
reserved IMDS budget; an exhausted budget prevents a POST and reports `not_sent`.

| Situation | Action |
| --- | --- |
| All checks pass/skipped | Execute application |
| Unknown, default `continue` | Log unknown and execute application |
| Unknown with `block`, no enforced failure | Stop, never reallocate solely for unknown |
| Confirmed failure, dry-run | Log `would_reallocate` and reason, then execute application unless unknown policy blocks |
| Confirmed failure, enforcement | Make at most one IMDS POST and stop |

A confirmed failure takes precedence over other unknown results when enforcement
is enabled. One failed check uses its own reason; multiple failures use
`MULTIPLE_FAILURE_REASON`. Every failed check and its reason remains in the logs.
Reasons are data only and cannot change check logic.

IMDS reasons use the [versioned reason contract](docs/reallocation-reasons.md):
`salad-preflight/v1 code=<stable-code> checks=<failed-checks>: <custom text>`.
Structured logs include `reason_code`; combined decisions also list every failed
check. The complete wire reason is capped at 1000 Unicode characters by shortening
only the custom text when necessary.

IMDS `204` means accepted. HTTP 3xx/4xx except `408` is rejected; redirects are
never followed. Transport errors, timeouts, `408`, 5xx, and unexpected 2xx are
uncertain: a request may have taken effect. Accepted, rejected, uncertain and
not-sent outcomes all stop this startup attempt. There is no retry of an IMDS POST,
no application fallback after an enforced failure, and no attempt to interpret
an untrusted response body. Investigate the logged status before restarting.

| Wrapper exit code | Meaning |
| --- | --- |
| 2 | Invalid configuration or missing application command |
| 20 | Reallocation accepted; application not started |
| 21 | Reallocation rejected; application not started |
| 22 | IMDS uncertain or budget exhausted before POST; application not started |
| 23 | Unknown policy blocked application |
| 24 | Startup interrupted |
| 126 / 127 | Application could not be executed / found |

Once executed, the application's exit status is its own and can overlap these
codes; use structured events to distinguish them. The wrapper exits after an
accepted request rather than waiting indefinitely for removal.

## Complete environment reference

Every key below has the prefix **`SALAD_PREFLIGHT_`**. Empty values behave as unset.
Booleans accept only lowercase `true` or `false`. Durations use Go duration syntax
(`500ms`, `15s`, `2m`), must be positive and at most `10m`, except `RETRY_DELAY`
may be zero. All supplied values are validated even for disabled checks. Unknown
prefixed keys are rejected to catch typos. Other environment variables are
neither interpreted nor logged, with one exception: `SALAD_MACHINE_ID`,
`SALAD_INSTANCE_ID` and `SALAD_CONTAINER_GROUP_ID`, which SaladCloud sets in every
container, are copied onto each event when present.

| Key (after prefix) | Default | Meaning |
| --- | --- | --- |
| `DRY_RUN` | `true` | Suppress IMDS POST; log proposed decision. Does not bypass `UNKNOWN_POLICY=block` |
| `UNKNOWN_POLICY` | `continue` | `continue` or `block`; unknown never triggers reallocation |
| `STARTUP_TIMEOUT` | `120s` | Total preflight and IMDS budget |
| `PROBE_TIMEOUT` | `15s` | Per measurement deadline |
| `IMDS_TIMEOUT` | `5s` | POST deadline, also limited by remaining startup budget |
| `ATTEMPTS` | `3` | Maximum measurements per enabled check, integer 1–10 |
| `FAIL_CONFIRMATIONS` | `2` | Consecutive low measurements required, integer 1–ATTEMPTS |
| `RETRY_DELAY` | `1s` | Wait between measurements |
| `GPU` | `0` | One `nvidia-smi` GPU index, UUID or PCI bus ID |
| `NVIDIA_SMI` | `nvidia-smi` | Executable name in PATH or absolute path; no shell arguments |
| `MULTIPLE_FAILURE_REASON` | `Multiple startup requirements not met` | IMDS reason when two or more checks fail |

Each check has an independent enable switch and reason. An unset enable switch
automatically enables a check when its threshold is set. `false` disables the
check even with a threshold; `true` requires a threshold. All thresholds are
unset by default and must be finite positive numbers, except version thresholds.
Equality passes. MiB means 1,048,576 bytes; Mbps means 1,000,000 bits/second;
power uses watts; temperature uses degrees Celsius; latency uses milliseconds;
the benchmark score has whatever unit your benchmark prints.

| Enable key | Threshold key | Reason key | Default reason |
| --- | --- | --- | --- |
| `GPU_TOTAL_ENABLED` | `GPU_TOTAL_MIN_MIB` | `GPU_TOTAL_REASON` | `GPU total VRAM below minimum` |
| `GPU_FREE_ENABLED` | `GPU_FREE_MIN_MIB` | `GPU_FREE_REASON` | `GPU free VRAM below minimum` |
| `GPU_POWER_ENABLED` | `GPU_POWER_MIN_WATTS` | `GPU_POWER_REASON` | `GPU power limit below minimum` |
| `GPU_DRIVER_ENABLED` | `GPU_DRIVER_MIN_VERSION` | `GPU_DRIVER_REASON` | `GPU driver version below minimum` |
| `GPU_CUDA_ENABLED` | `GPU_CUDA_MIN_VERSION` | `GPU_CUDA_REASON` | `CUDA version below minimum` |
| `GPU_UTILIZATION_ENABLED` | `GPU_UTILIZATION_MAX_PERCENT` | `GPU_UTILIZATION_REASON` | `GPU utilization above maximum` |
| `GPU_TEMPERATURE_ENABLED` | `GPU_TEMPERATURE_MAX_CELSIUS` | `GPU_TEMPERATURE_REASON` | `GPU temperature above maximum` |
| `LATENCY_ENABLED` | `LATENCY_MAX_MS` | `LATENCY_REASON` | `Network latency above maximum` |
| `DOWNLOAD_ENABLED` | `DOWNLOAD_MIN_MBPS` | `DOWNLOAD_REASON` | `Download bandwidth below minimum` |
| `UPLOAD_ENABLED` | `UPLOAD_MIN_MBPS` | `UPLOAD_REASON` | `Upload bandwidth below minimum` |
| `BENCHMARK_ENABLED` | `BENCHMARK_MIN_SCORE` | `BENCHMARK_REASON` | `Benchmark score below minimum` |

Version thresholds are one to three dot-separated numbers, each from 0 to 999,
such as `12.8` or `550.54.14`. They compare number by number, so `12.10` is newer
than `12.8`.

Free VRAM and power also accept percentage thresholds:

```sh
SALAD_PREFLIGHT_GPU_FREE_MIN_PERCENT=95
SALAD_PREFLIGHT_GPU_POWER_MIN_PERCENT=90
SALAD_PREFLIGHT_GPU_POWER_REFERENCE=default
```

`GPU_FREE_MIN_PERCENT` compares `100 × memory.free / memory.total` for the
selected GPU. `GPU_POWER_MIN_PERCENT` compares `100 × power.limit / reference`;
`GPU_POWER_REFERENCE` defaults to `default`, which uses `power.default_limit`; `max` uses
`power.max_limit`. The default reference is the normal default ceiling; the
maximum adjustable limit may be higher. This is configured power headroom,
not idle draw or proof of sustained performance. Both percentages must be greater
than zero and at most 100. Equality passes; measured power above 100% is valid.

Choose one threshold mode per check: `GPU_FREE_MIN_PERCENT` cannot coexist with
`GPU_FREE_MIN_MIB`, and `GPU_POWER_MIN_PERCENT` cannot coexist with
`GPU_POWER_MIN_WATTS`. Existing enable/reason keys work in either mode. Each
percentage attempt reads the numerator and reference together in one GPU query.
Missing or invalid reference telemetry makes the result unknown; no guessed GPU
TDP or substituted denominator is used. Percentage events log `measurement` and
`threshold` in percent, plus `observed_amount`, `reference_amount`, `source_units`
and `reference_field` so the raw MiB/watts are reviewable.
For a strict requirement, also set `SALAD_PREFLIGHT_UNKNOWN_POLICY=block`: an
unavailable percentage then blocks startup without requesting reallocation.

All reason strings must have 1–1000 Unicode characters, some non-whitespace text,
and no control characters. **Reasons appear in logs and are sent to SaladCloud
support when enforcing**. Do not put credentials or private customer data in them.

| Key (after prefix) | Default | Meaning |
| --- | --- | --- |
| `DOWNLOAD_URL` | unset | Required for enabled download; HTTP(S), no URL userinfo or fragment |
| `DOWNLOAD_BYTES` | `25000000` | Exact expected download object size, integer bytes 1–1,073,741,824 |
| `DOWNLOAD_SHA256` | unset | Required for enabled download: expected object's 64-digit hex SHA-256 |
| `DOWNLOAD_BEARER_TOKEN` | unset | Optional Authorization bearer token, printable ASCII with no whitespace |
| `UPLOAD_URL` | unset | Required for enabled upload; HTTP(S), no URL userinfo or fragment |
| `UPLOAD_BYTES` | `25000000` | Generated body size, integer bytes 1–1,073,741,824 |
| `UPLOAD_BEARER_TOKEN` | unset | Optional Authorization bearer token, same restrictions |
| `LATENCY_ADDRESS` | unset | Required for enabled latency: `host:port` to connect to, such as `storage.example.com:443` |
| `LATENCY_SAMPLES` | `5` | Timed connections per measurement, integer 1–20 |
| `BENCHMARK_COMMAND` | unset | Required for enabled benchmark: one executable name in PATH or path; no arguments, no shell |
| `BENCHMARK_TIMEOUT` | `60s` | Deadline for each benchmark run, in place of `PROBE_TIMEOUT` |

There are no default network endpoints and no IMDS URL override. TLS certificates
are verified using the container's trust store; there is no insecure TLS switch.
All HTTP traffic bypasses environment proxies, including IMDS, and latency
connections are plain TCP. This makes network measurements direct; proxy-only environments will report unknown. HTTP endpoints
send bearer tokens in clear text; use HTTPS for authenticated external endpoints.

## Bandwidth endpoint contract

Download sends one GET per attempt, with `Accept-Encoding: identity` and cache
control requesting no cache. Your endpoint must return `200`, content type
`application/octet-stream`, exact `Content-Length=DOWNLOAD_BYTES`, no
`Content-Encoding`, and a body matching `DOWNLOAD_SHA256`. Redirects, HTML errors
(even a 200 error page), short bodies, different content and compressed responses
are unknown. Provide a fixed binary object and obtain its size and SHA-256 before
enabling the check. Chunked downloads without Content-Length are unsupported.

Upload sends a POST of exactly `UPLOAD_BYTES` freshly generated random bytes,
streamed with content type `application/octet-stream` and Content-Length. It never
reads application files. The server must consume the entire request and then
return `200`, content type `application/json`, no Content-Encoding, and a JSON
acknowledgement such as `{"received_bytes":25000000}` matching the actual byte
count. The response must be at most 4096 bytes. Missing/mismatched acknowledgements,
other statuses and connection failures are unknown. Upload endpoints must tolerate
repeated test uploads and discard the data; measurement POSTs can be retried.

Transfers use bounded buffers, a byte cap and an attempt deadline; an incomplete
transfer is unknown, not a low-speed failure. Maximum request/download payload per
run is `ATTEMPTS × (enabled DOWNLOAD_BYTES + enabled UPLOAD_BYTES)`, with headers
and acknowledgements additional. Each attempt uses a fresh connection. The rate
is payload bits divided by wall time from before the request through reading and
validating the full response. It includes DNS, connection/TLS setup, latency,
generation/hashing and server acknowledgement time. This is application-level
throughput to your endpoint, not an isolated measurement of the node's link speed.

Use endpoints with spare capacity, no intentional rate limits or CDN compression,
and appropriate geographic placement. Too-small payloads overemphasize latency:
connection setup and TCP ramp-up are part of the measured time, so a small
transfer to a distant endpoint reports far less than the link can carry. The
25 MB default matches SaladCloud's own
[bandwidth check example](https://docs.salad.com/container-engine/tutorials/performance/network-bandwidth-checks).
Too-large payloads may time out on slower nodes. If the timeout is shorter than
the transfer time at your minimum rate, bad links may remain unknown. For example,
allow at least `bytes × 8 / (minimum Mbps × 1e6)` seconds plus setup overhead.
**With the defaults (25,000,000 bytes and `PROBE_TIMEOUT=15s`) any link slower
than about 13.3 Mbps cannot finish in time and is unknown, not a failure.** For a
minimum near or below that, raise `PROBE_TIMEOUT` or lower the byte count.
An unreachable endpoint cannot establish that the node is bad. A valid slow
response can also reflect server load; validate settings across representative
nodes before enforcing.

## Latency check

Each measurement opens one untimed TCP connection to `LATENCY_ADDRESS`, which
resolves the name and settles on one IP address, then times `LATENCY_SAMPLES`
more connections to that same address and reports the median in milliseconds.
Opening a TCP connection takes about one network round trip, so this approximates
round-trip time without needing ICMP or root. It is not `ping`: it includes a
little local overhead, and no data is sent and no TLS is negotiated. Name lookup
is not part of the measurement. The median ignores an occasional slow or
retransmitted connection.

Point it at the service your workload talks to, such as your storage region's
endpoint on port 443. Any refused, unreachable or timed-out connection makes the
whole measurement unknown. Each measurement makes `LATENCY_SAMPLES + 1` short
connections that are closed immediately.

## Benchmark check

`BENCHMARK_COMMAND` names one executable that measures what your workload needs,
for example a short inference warm-up, and prints the result. Nodes with the same
GPU differ in CPU, cooling and power limits, and a benchmark covers all of them
with one number. The contract:

- It runs with no arguments and no shell, with the wrapper's environment and
  working directory. It sees the same variables your application will, including
  any tokens. Use a script or the environment to configure it.
- It must exit `0` and print exactly one non-negative number to stdout, at most
  4096 bytes. Higher is better. Its stderr is discarded.
- A nonzero exit, any other output, or running past `BENCHMARK_TIMEOUT` is
  unknown. **A printed number is treated as a real measurement**, so on an error
  exit nonzero; do not print `0`.
- At the deadline, or if startup is interrupted, the wrapper kills the benchmark
  and every process in its process group. Processes a benchmark leaves behind
  after exiting normally are not cleaned up.

A pass needs one run. A confirmed failure needs `FAIL_CONFIRMATIONS` runs, and
unknown results can use all `ATTEMPTS`. Benchmark time counts against
`STARTUP_TIMEOUT` and is billed like any other running time: three attempts at
the default 60s do not fit in the default 120s budget. Keep the benchmark short,
and remember the first inference after loading a model is usually slow, so
exclude it from the number you print.

## GPU and image limitations

The wrapper queries `memory.total`, `memory.free`, and **`power.limit`**, separately,
using `--id`, `--query-gpu`, and `--format=csv,noheader,nounits`. The power check
compares the reported configured power ceiling, **not idle `power.draw`**, nor
the GPU's maximum adjustable limit, TDP or a performance benchmark. It does not
change power limits, clocks, fans or any other host setting. Unsupported fields,
`N/A`, malformed/multiple rows, errors, and missing tools are unknown. Zero free
memory is valid; zero total memory or zero power limit is treated as unavailable.

The driver check queries `driver_version`. The CUDA check reads `CUDA UMD Version`
or the older `CUDA Version` label from the header of `nvidia-smi`'s summary output.
[NVIDIA documents both labels](https://docs.nvidia.com/deploy/nvidia-smi/index.html)
as the newest CUDA version the node's driver supports, not the toolkit
inside your image. A header that shows `N/A` or has no CUDA version is unknown.
SaladCloud's [performance guide](https://docs.salad.com/container-engine/tutorials/performance/high-performance-apps)
stated on 2026-11-11 that allocated nodes support CUDA Toolkit 12.8 and later,
and that a small share of nodes run older drivers; check only if your image
needs something newer.

The utilization check queries `utilization.gpu` (percent). It is a snapshot of
what else is using the GPU before your application starts; zero is valid and a
reading above 100 is unknown. The temperature check queries `temperature.gpu`.
At startup the GPU is mostly idle, so this catches a GPU that is already hot, not
one that will overheat under your load. Zero or a reading above 150 is unknown.

Linux containers are supported. The static wrapper itself works with glibc, musl,
scratch and distroless, without root, Python, curl, jq, CUDA SDK or a shell. HTTPS
needs CA certificates in the final image. GPU checks additionally require an
accessible NVIDIA GPU, driver libraries and a working `nvidia-smi` executable;
the wrapper does not install a host driver. NVIDIA utilities may need glibc even
when the wrapper runs on Alpine. Scratch/distroless images typically need extra
work for GPU tools. Network-only checks work without NVIDIA dependencies.

`GPU` selects the `nvidia-smi` device view for every GPU check, which is not necessarily remapped by
`CUDA_VISIBLE_DEVICES`. Verify it matches your application's GPU; UUIDs are useful
for unambiguous selection. This version checks one GPU and does not aggregate
devices or provide MIG/per-process accounting guarantees. Free VRAM is a startup
snapshot and can change before allocation. AMD and other non-NVIDIA GPUs are not
supported by these GPU probes. WSL and driver-specific telemetry can be incomplete;
the wrapper does not infer missing power values from power draw. See NVIDIA's
[nvidia-smi reference](https://docs.nvidia.com/deploy/nvidia-smi/index.html) and
[WSL limitations](https://docs.nvidia.com/cuda/wsl-user-guide/index.html).

## IMDS contract and diagnostics

Verified against the official [Reallocate Container Instance reference](https://docs.salad.com/reference/imds/reallocate)
on 2026-11-09: `POST http://169.254.169.254/v1/reallocate`, `Metadata: true`,
`Content-Type: application/json`, body `{"reason":"..."}`, success `204 No Content`.
The reason has a documented length of 1–1000 characters and is reported to
SaladCloud support. This implementation uses a direct client with no redirects,
proxy, idempotency key, automatic replay body or POST retry loop, and a deadline.

JSON Lines go to stderr. `measurement` events contain individual pass/fail/unknown
observations; only `check_result` events contain the final confirmation decision.
Every check has a final result, measurement (null when unavailable), threshold
(null when unset), and units. Maximum checks also carry `"comparison":"at_most"`;
its absence means the threshold is a minimum. Version checks report measurement
and threshold as `major × 1,000,000 + minor × 1,000 + patch` so they sort and
compare as numbers, with the readable versions in `measurement_text` and
`threshold_text`. Decisions carry `action` and relevant `reason`;
`imds_result` carries `imds.outcome` and an HTTP status when received. On
SaladCloud every event also carries `machine_id`, `instance_id` and
`container_group_id`, so results from many nodes can be grouped without relying
on labels added by a log pipeline. For example:

```json
{"event":"check_result","check":"gpu_free","result":"unknown","measurement":null,"threshold":8192,"units":"MiB","attempt":3,"detail":"gpu_telemetry_unavailable"}
{"event":"check_result","check":"gpu_cuda","result":"fail","measurement":12006000,"threshold":12008000,"units":"version","measurement_text":"12.6","threshold_text":"12.8","attempt":2,"reason":"CUDA version below minimum","reason_code":"gpu_cuda_below_minimum"}
{"event":"check_result","check":"latency","result":"pass","measurement":41.7,"threshold":150,"units":"ms","comparison":"at_most","attempt":1}
{"event":"decision","measurement":null,"threshold":null,"action":"launch"}
```

These example values are illustrative, not recommended thresholds. Logs omit
URLs, URL query parameters, the latency address, bearer tokens, response bodies,
raw command output other than a validated version, the benchmark command,
application arguments and unrelated environment variables. Error details are
fixed identifiers, not raw HTTP errors. Application logs remain the application's
responsibility. A partial failure streak can retain the last valid measurement
with an unknown final result; it is not sufficient evidence to reallocate.

## Tests and manual validation

```sh
go test -race ./...
go vet ./...
```

Tests mock GPU commands, stream HTTP through in-memory endpoints, inject
connections for latency, run small shell scripts as benchmarks, and inject
IMDS responses. They cover equality/low/high thresholds, version comparison,
missing/unsupported telemetry, configuration validation, deadlines, retries,
custom reasons, dry-run, multiple failures, credential redaction, proxy bypass,
benchmark process cleanup, node identity, accepted/rejected/uncertain IMDS,
and application execution. They do not contact real IMDS, reallocate nodes, or
run public bandwidth tests. Go's race detector needs a C toolchain on the test
host; the production binary is built with CGO disabled.

For deliberate real-environment testing, follow the separate
[opt-in manual validation procedure](docs/manual-validation.md). No live node or
bandwidth validation is part of the automated suite.

The [2026-11-11 live validation report](docs/validation-2026-11-11.md) records
39 passing scenarios on Salad, a verified machine replacement, the CUDA header
compatibility fix, and an observed IMDS 200-versus-documented-204 discrepancy.
The disposable [validation harness](validation/README.md) reproduces these checks.

The [2026-11-12 percentage test report](docs/validation-2026-11-12.md) records real
reallocations for less than 95% free VRAM and less than 90% of default GPU power,
followed by a replacement that passed both requirements. It includes the
published `20261112-v1` worker image and its immutable digest.

**Aggressive thresholds can reject too many nodes and reduce available capacity.**
SaladCloud [excludes a node you reallocate away from](https://docs.salad.com/container-engine/tutorials/performance/network-bandwidth-checks)
from your allocation pool for 48 hours. That stops a replica returning straight to
a node it rejected, but a threshold that is too strict, or a slow test endpoint,
can work through the available nodes and leave replicas unable to allocate until
exclusions expire.
Start with dry-run, compare observed values across your eligible fleet, and enable
only requirements your application actually needs. This version enforces one
request per process run only: it has **no durable cooldown, cross-restart counter,
fleet coordination or prevention of repeated reallocation across restarts or
machines**. Container restart policies may rerun the wrapper after any exit,
including accepted or uncertain requests. Plan external retry limits/cooldowns
before enforcing across a fleet.
