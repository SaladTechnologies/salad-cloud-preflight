# Opt-in manual validation

These steps are deliberately separate from automated tests. They require your
own endpoints and, for the final step, a disposable SaladCloud instance. Do not
run the enforcement step on a production workload. No validation step has been
run against a real Salad node by the automated suite.

1. Build the binary and copy it into your actual customer image. Leave
   `SALAD_PREFLIGHT_DRY_RUN=true`. With every threshold unset, launch a harmless
   application such as `/bin/echo preflight-complete` (or your image's equivalent).
   Confirm eleven skipped results and application execution.
2. In that image on a GPU node, inspect `nvidia-smi --id=0
   --query-gpu=memory.total,memory.free,power.limit,driver_version,utilization.gpu,temperature.gpu
   --format=csv,noheader,nounits`, and run plain `nvidia-smi --id=0` to see the
   `CUDA UMD Version` (or older `CUDA Version`) in its header. Choose the correct index/UUID for your
   application's GPU. Enable one threshold at a time based on your own application
   requirements. Confirm matching MiB, watts, versions, percent or degrees in JSON
   logs. Unsupported power telemetry must be unknown, never a measured low power
   draw, and a header showing `CUDA Version: N/A` must be unknown.
3. Validate unknown handling in dry-run: set `SALAD_PREFLIGHT_NVIDIA_SMI` to a
   nonexistent path. Confirm unknown results, retries and application execution
   under `continue`; repeat with `UNKNOWN_POLICY=block` and confirm exit 23. Restore
   the real utility path before continuing.
4. Only if you intend to test bandwidth, create a fixed binary object you own
   (for example, `head -c 25000000 /dev/urandom > probe.bin`). Record its exact size
   with `wc -c < probe.bin` and digest with `sha256sum probe.bin`. Serve it over
   HTTPS with the README's download response contract. Configure its URL, byte
   count, SHA-256, and a deliberately chosen minimum. Supply any bearer token
   through secrets management. Confirm the full transfer finishes before the
   probe deadline and the measured rate is plausible. This consumes real traffic.
5. For upload, provision an endpoint you control that consumes and discards the
   full POST body and returns JSON with the received byte count. Set URL, bytes
   and minimum, then confirm bounded traffic and valid acknowledgements. Verify
   a bad digest, HTML response, non-200 response, mismatched acknowledgement and
   unreachable endpoint each produce unknown. Never use an unrelated public
   endpoint for upload tests.
6. Only if you intend to check latency, set `SALAD_PREFLIGHT_LATENCY_ADDRESS` to
   the `host:port` your workload uses and a generous `LATENCY_MAX_MS`. Confirm a
   plausible median in milliseconds, then point it at a closed port and confirm
   unknown. For a benchmark, set `BENCHMARK_COMMAND` to your executable and
   confirm its printed number appears as the measurement, that a nonzero exit is
   unknown, and that a run longer than `BENCHMARK_TIMEOUT` is stopped and unknown
   with none of its processes left running.
7. Still in dry-run, temporarily set a threshold above the observed value.
   Confirm two consecutive low measurements, a final fail, `would_reallocate`
   with your custom reason, no IMDS POST, and application execution. Do the same
   for a maximum check with a threshold below the observed value. Enable two
   such failures and confirm the combined reason. Restore suitable thresholds
   afterward. Test cancellation during a probe and normal application signal
   and exit behavior in your target runtime.
8. **Optional live reallocation:** explicitly opt in only on a disposable
   SaladCloud instance with restart/reallocation limits managed externally. Set
   `SALAD_PREFLIGHT_DRY_RUN=false`, one known failing threshold, and a short
   non-sensitive reason identifying this validation. Expect at most one POST,
   `204`/accepted, exit 20, and no application start. Confirm the reallocation
   in SaladCloud. If the outcome is rejected, fix the underlying issue before
   restarting. If uncertain, inspect SaladCloud state before restarting; the
   POST may have succeeded. Do not blindly rerun it.
9. Restore dry-run and production-intended thresholds. Review fleet-wide dry-run
   observations, endpoint capacity, startup time and available node capacity
   before enabling enforcement. Configure external restart limits/cooldowns;
   this binary stores no cross-run reallocation history.

Record the image digest, platform/driver version, enabled checks, sanitized
observations, elapsed startup time and outcomes. Do not paste bearer tokens,
signed URLs or private reasons into shared logs or reports.
