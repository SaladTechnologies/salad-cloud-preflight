# Percentage thresholds and real reallocation — 2026-11-12

Both requested scenarios caused a real IMDS reallocation on Salad. Each used a
natural hardware shortage, and each replacement was verified ready on a different
machine. The final replacement passed both requirements together.

## Configuration and meaning

```sh
SALAD_PREFLIGHT_GPU_FREE_MIN_PERCENT=95
SALAD_PREFLIGHT_GPU_POWER_MIN_PERCENT=90
SALAD_PREFLIGHT_GPU_POWER_REFERENCE=default
SALAD_PREFLIGHT_UNKNOWN_POLICY=block
SALAD_PREFLIGHT_DRY_RUN=false
```

These percentage thresholds were added for this test. Existing absolute MiB/watt
thresholds remain supported, but cannot be combined with percentage thresholds
for the same check. Equality passes. Two consecutive failures are required by
default. Unknown telemetry blocks startup with the configuration above, without
requesting reallocation solely for an unknown result.

Free VRAM is `100 × memory.free / memory.total`. Power is
`100 × power.limit / power.default_limit`. The default reference is the GPU's
normal power ceiling, as described in
[NVIDIA's documentation](https://docs.nvidia.com/deploy/nvidia-smi/index.html).
This checks the configured power limit; it does not establish sustained power
draw or application throughput. `GPU_POWER_REFERENCE=max` instead compares with
the maximum adjustable limit, which can be higher than the default.

The checks read numerator and denominator together for the selected GPU. Events
include both raw quantities, their units, the reference field, and the percentage.
No GPU power, clock, or fan settings were changed.

## Live results

| Scenario | Actual reading | Requirement | Outcome |
| --- | --- | --- | --- |
| VRAM failure | 11035 / 12288 MiB = **89.8031%** free | At least 95% | Real IMDS reallocation; different ready machine verified |
| Power failure on replacement | 136 / 170 W = **80%** of default | At least 90% | Real IMDS reallocation; different ready machine verified |
| Final replacement, both checks | 11784 / 12288 MiB = **95.8984%**; 170 / 170 W = **100%** | 95% VRAM and 90% power | Both passed together; application marker launched; no reallocation requested |

The initial node also passed the power check at 170/170 W. Its maximum adjustable
limit was 212 W, illustrating why the power reference matters: 170/212 is 80.19%,
even though the GPU was configured at its normal default ceiling. Both actual
power tests used the **default** reference.

The VRAM enforcement used `percent.py vram --natural`; no extra memory was held
during that enforcement. A separate dry-run exercised a temporary 10% allocation,
and local testing verified that it was released afterwards.

Only the relevant check was enabled for each failure scenario. Each dry-run
confirmed two consecutive failures before enforcement. The power enforcement
response itself also captured two 80% readings, `gpu_power_below_minimum`, and
`application_started: false`.

## Independent replacement evidence

| Stage | Machine | Instance |
| --- | --- | --- |
| Before VRAM test | `066a0696-21f5-6654-93e7-1bb9ce8c11a7` | `a31a68f2-8468-44d5-9761-d38938e06d2c` |
| After VRAM / before power | `b5d93150-453a-515f-91e8-53f8b713ddd8` | `bf98f0ff-197b-419a-abfb-631de354c182` |
| After power | `6f96eeea-849a-2a5e-9845-3f67b1840f4d` | `c665fa22-5f5f-4ed7-accd-8f3ea5d87c71` |

Salad recorded `Instance Reallocated by IMDS Request` for the VRAM test at
`2026-11-12T08:13:51.0977647Z` and for the power test at
`2026-11-12T08:19:32.0733144Z`. For each replacement, the API reported readiness,
its identities matched the service's `/info`, and its test history was empty
before further checks. Tests do not run automatically on replacement.

The VRAM enforcement's gateway response timed out during replacement. Its exact
IMDS response status and child exit code were not captured; no result is invented
for them. The platform event and independently verified replacement establish
that the request took effect. The durable attempt marker was retained and the
request was never retried.

The power enforcement returned IMDS **HTTP 200**, while the
[official IMDS documentation](https://docs.salad.com/reference/imds/reallocate)
still specifies **204** (rechecked on November 12). The wrapper classified that
response as `uncertain`, returned **exit 22**, blocked the application, and did
not retry. Platform logs and the ready replacement independently confirm success.
Strict response classification remains unchanged. The documented 204/exit-20
branch is covered by automated tests, but was not observed on these live nodes.

The standardized reasons were:

```text
salad-preflight/v1 code=gpu_free_below_minimum checks=gpu_free: Validation: at least 95% VRAM free
salad-preflight/v1 code=gpu_power_below_minimum checks=gpu_power: Validation: configured power limit below 90% of default
```

## Images and test scope

| Artifact | Image | Digest |
| --- | --- | --- |
| Reusable worker | `saladtechnologies/salad-cloud-preflight:20261112-v1` | `sha256:644fae3d6f287ee1dd0d890873e5684c951d953da1a60551d34b42071443beb9` |
| Validation service | `saladtechnologies/salad-preflight-validation:20261112-v1` | `sha256:001b5eae7ce84ef17da79169b4be11138a73ef79ecb8f95e5511d7b338b7c016` |

The same disposable group, `preflight-validation-20261111`, was updated to
version 5 in `salad-benchmarking` / `recipe-staging`: one RTX 3060 (12 GB), two
vCPUs, 2048 MB RAM, and 5 GiB disk. The account API key remained local. The
validation service runs checks only when explicitly requested over its
authenticated test endpoint; the environment settings above were supplied to
each worker invocation.

`go test -race ./...` and `go vet ./...` passed after the percentage implementation.
Automated tests cover exact boundaries, just-below failures, missing/invalid
references, mode conflicts, selected-GPU paired queries, reason codes, and mocked
IMDS enforcement. Local container tests exercised real GPU measurements and
temporary memory allocation cleanup. The eleven-check matrix from November 11
is documented separately; it was not rerun as part of this focused test.

These results demonstrate both requested checks and completed reallocations on
the tested nodes. They do not guarantee telemetry support or sustained workload
performance on every possible GPU/driver combination.

## Evidence and cleanup

Sanitized raw evidence is local and Git-ignored in `artifacts/percent-20261112/`:

- `vram-dry.json`, `vram-attempt.json`, `vram-enforcement-console.log`.
- `vram-platform-events.json`, `vram-all-events.json`, `vram-verified.json`.
- `power-dry.json`, `power-enforced.json`, `power-attempt.json`.
- `power-all-events.json`, `power-platform-events.json`, `power-verified.json`.
- `diagnostics.json`, `final-diagnostics.json`, `final-both-dry.json`, `local.json`.

The local GPU validation container was stopped and its removal verified after testing.
The cloud test group was stopped at the user's request. The API confirmed
`stopped`, zero running instances, and an empty instance list at
`2026-11-12T12:00:33.4689818Z`. The stopped group is retained for inspection.
No further reallocation tests are needed for these two scenarios.
