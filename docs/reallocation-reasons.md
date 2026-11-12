# Reallocation reason contract v1

Salad IMDS accepts a JSON object with one required `reason` string of 1–1000
characters. The [official contract](https://docs.salad.com/reference/imds/reallocate)
does not enumerate reason codes (verified 2026-11-11). These codes are this
worker's convention, not a Salad platform enum.

The worker sends:

```json
{"reason":"salad-preflight/v1 code=gpu_free_below_minimum checks=gpu_free: GPU free VRAM below minimum"}
```

The envelope is fixed. The suffix is the existing configurable `*_REASON` text.
Changing that text cannot change the code or decision. Consumers should split at
the first `: ` and parse only the envelope; the text can contain arbitrary Unicode
and punctuation. The complete string is capped at 1000 Unicode characters. If
necessary, only the message is shortened and ends with `…`.

| Check | Stable code |
| --- | --- |
| Total VRAM | `gpu_total_below_minimum` |
| Free VRAM | `gpu_free_below_minimum` |
| Configured GPU power ceiling | `gpu_power_below_minimum` |
| Driver version | `gpu_driver_below_minimum` |
| Driver-supported CUDA version | `gpu_cuda_below_minimum` |
| GPU utilization | `gpu_utilization_above_maximum` |
| GPU temperature | `gpu_temperature_above_maximum` |
| TCP latency | `latency_above_maximum` |
| Download throughput | `download_below_minimum` |
| Upload throughput | `upload_below_minimum` |
| Supplied benchmark score | `benchmark_below_minimum` |
| Two or more confirmed failures | `multiple_checks_failed` |

Multiple failures use `MULTIPLE_FAILURE_REASON` and include every failed check in
evaluation order, for example:

```text
salad-preflight/v1 code=multiple_checks_failed checks=gpu_free,gpu_power,download: Multiple startup requirements not met
```

`check_result` logs retain the full custom message and add `reason_code` for
confirmed failures. Decision and IMDS-result logs carry the exact transmitted
`reason`, its `reason_code`, and `failed_checks`. Measurements, thresholds, units,
attempts and instance/machine/group IDs remain in the structured logs. Unknown
and skipped results never receive a failure code and never trigger IMDS alone.

Dry-run produces the same formatted reason that enforcement would send. Reasons
are visible to Salad support; do not include secrets, URLs, or private workload
data. This changes the wire reason from earlier builds that sent only custom
text; update any exact-string log filters to use `reason_code` or the envelope.

Percentage thresholds use the same `gpu_free_below_minimum` and
`gpu_power_below_minimum` codes as absolute thresholds. Logs distinguish the mode
through `units=percent`, `reference_field`, and the raw observed/reference amounts.
