# Salad preflight validation — 2026-11-11

Completed the live validation: **39/39 dry-run scenarios passed on Salad**, and
one enforced IMDS request moved the instance to a different, ready machine.
The application was blocked after enforcement. No request was retried.

**Cleanup:** the user stopped the disposable Salad group. Its stopped state was
verified through the API on 2026-11-12, before preparing further tests.
The local validation container was stopped and removed. No production groups were changed.

## Images and deployment

| Artifact | Published image | Digest |
| --- | --- | --- |
| Reusable static worker, about 3 MB | `saladtechnologies/salad-cloud-preflight:20261111-v2` | `sha256:ee73bac3f4cebe1ff505f7248564106f28ebf4c1f169bd289fce68e6322e2993` |
| Validation service, about 47 MB | `saladtechnologies/salad-preflight-validation:20261111-v4` | `sha256:08f92b552469292dd455ed927a4f165f69f2ce4423f4a950d98b507297dd9f3c` |

Organization/project: `salad-benchmarking` / `recipe-staging`.
Group: `preflight-validation-20261111` (`be1eaf53-5df0-4283-90d0-5d0000e07b6e`).
One RTX 3060 (12 GB), 2 vCPUs, 2048 MB RAM, 5 GiB disk, high priority.
The group runs the validation image by digest, with application bearer
 authentication, IPv4/IPv6 support and a readiness probe. Its only custom persistent
secret is a generated test token; the Salad account key stays local.

The reusable image exports `/salad-preflight` for copying into a customer image.
It is not the Python test service. Earlier worker `20261111-v1` and validation
`v1`–`v3` images were superseded during this investigation; use the versions above.

## Check coverage and observations

| Check | Passing-run observation | Live coverage |
| --- | --- | --- |
| `gpu_total` | 12288 MiB | Pass / fail / unknown verified |
| `gpu_free` | 11717 MiB | Pass / fail / unknown verified |
| `gpu_power` | 170 watts | Pass / fail / unknown verified |
| `gpu_driver` | 610.88 version | Pass / fail / unknown verified |
| `gpu_cuda` | 13.3 version | Pass / fail / unknown verified |
| `gpu_utilization` | 0 percent | Pass / fail / unknown verified |
| `gpu_temperature` | 56 celsius | Pass / fail / unknown verified |
| `latency` | 7.545 ms | Pass / fail / unknown verified |
| `download` | 18.387 Mbps | Pass / fail / unknown verified |
| `upload` | 21.822 Mbps | Pass / fail / unknown verified |
| `benchmark` | 2254.867 score | Pass / fail / unknown verified |

All eleven checks were enabled individually using environment variables. Thresholds
were deliberately permissive for pass cases and impossible for failure cases;
they are test inputs, not recommended production thresholds. Unknown cases used
missing tools, a closed port, incorrect download digest, invalid upload
acknowledgement or missing benchmark executable. Additional cases covered all
checks disabled, unknown blocking (exit 23), combined failure reasons, custom
reason text, an HTML download response and benchmark timeout.

For failures, the wrapper required two consecutive measurements, reported
`would_reallocate`, made no IMDS call and launched the application marker. The
utilization failure used a short real CUDA memory workload; it measured 100%.
The benchmark is real CPU SHA-256 throughput, in MiB/s, exercising the executable
contract. It does not establish inference performance.

Network tests transferred generated 4 MiB payloads through the group's HTTPS
Container Gateway back to its own fixture. They validate requests, response
integrity and threshold decisions over a real network path. The rates include
both directions and gateway/TLS overhead; they do not independently establish
node uplink/downlink capacity or suitable production thresholds.

## Live reallocation and status discrepancy

The single enforced request used:

```text
salad-preflight/v1 code=gpu_free_below_minimum checks=gpu_free: Controlled preflight validation; deliberately impossible threshold
```

The live IMDS service returned **HTTP 200**, while the
[current official IMDS schema](https://docs.salad.com/reference/imds/reallocate)
still lists **204** as its success status (rechecked after this observation).
According to the worker's existing contract, 200 is `uncertain`: the wrapper
returned **exit 22**, blocked application execution and did not retry. A real
204/exit-20 response was not observed; that branch is covered by automated tests.

The Salad system event at `2026-11-11T20:16:00.6198522Z` reports
`Instance Reallocated by IMDS Request`. Read-only verification then confirmed:

| Identity | Before | After |
| --- | --- | --- |
| Machine | `05149a54-6030-e559-8338-7b82acbf609a` | `ded6771d-be93-7951-98fc-880da377d1b9` |
| Instance | `401399f1-61b3-486f-ac0f-43734ad4673e` | `70e4f709-c290-4b78-9486-957107c1f57b` |

The replacement was ready, and its `/info` matched the API identities. Its test
history was empty: no checks or reallocation ran automatically after replacement.
This proves the live request took effect despite the unexpected response status.
The strict IMDS status handling remains unchanged pending a platform contract
clarification; operators must inspect cloud state after an uncertain result.

## Fixes found during validation

The first live matrix passed 37/39 scenarios. NVIDIA 610 reports
`CUDA UMD Version: 13.3`; the worker recognized only the old `CUDA Version:` label.
[NVIDIA documents the rename](https://docs.nvidia.com/deploy/nvidia-smi/index.html).
The parser now recognizes both, still rejects unavailable/malformed versions, and
has regression coverage from the captured live header. The final full matrix
passed 39/39. The validation HTTP service was also corrected to bind dual-stack
IPv6, required by Salad's gateway.

The [reason contract](reallocation-reasons.md) defines stable per-check codes,
`multiple_checks_failed`, a versioned wire envelope, custom text, and Unicode-safe
1000-character truncation. Unknown results never acquire a failure code by
themselves. Unit tests verify all eleven enforcement paths with mocked IMDS.

## Verification and evidence

- `go test -race ./...` and `go vet ./...`: passed after the final Go change.
- Final image local GPU/container matrix: 39/39 passed, using the older CUDA label.
- Final Salad GPU/container matrix: 39/39 passed, using the new CUDA UMD label.
- One real IMDS request; application blocked; different ready machine verified.
- No retries after the unexpected live HTTP 200 response.

Reproduction scripts and limitations: [validation/README.md](../validation/README.md).
Raw sanitized evidence is local and Git-ignored:

- [Final Salad matrix](../artifacts/salad-matrix-final.json)
- [Final local matrix](../artifacts/local-matrix-final.json)
- [Enforced result](../artifacts/enforcement.json)
- [System reallocation events](../artifacts/reallocation-system-logs.json)
- [Verified replacement](../artifacts/reallocation-verified.json)
- [NVIDIA diagnostic header](../artifacts/nvidia-diagnostics.json)

The original failing matrix is retained as `artifacts/salad-matrix-v2.json`.
Credentials and test tokens are excluded from Git, Docker context, and this report.
