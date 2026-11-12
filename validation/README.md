# Disposable Salad validation

This image tests the real startup wrapper on a GPU node. It contains a Python
HTTP service, generated-data download/upload fixtures, a CPU hashing benchmark,
and a short CUDA memory workload for testing nonzero GPU utilization. These
helpers are excluded from the reusable binary image.

No checks run automatically. `/run` invokes the real wrapper with environment
variables and `/bin/echo PREFLIGHT_APP_STARTED` as its application. Dry-run is
forced unless the authenticated request explicitly sets `enforce: true`.
The server permits at most one enforcement attempt per process lifetime. The
controller's exclusive local marker prevents accidental repetition after node
replacement. This is a validation safeguard, not production reallocation history.

The percentage test controller in `percent.py` supports the specific 95% free
VRAM and 90% power requirements. It uses real paired GPU telemetry, checks the
failure in dry-run first, then allows one enforcement per scenario. The default
power reference is `power.default_limit`; `--power-max` explicitly selects the
maximum adjustable limit instead. It never changes GPU power, clocks, or fans.

All endpoints except `/health` require a random 32+ character `SALAD_TEST_TOKEN`.
Only that test token is injected into the group. The account API key stays in
the local, ignored `.env.salad`. The gateway uses application bearer authentication;
Salad's account-key gateway authentication is disabled to avoid putting the
account key on test nodes. Use TLS for remote access.

## Run

Build `validation/Dockerfile` for `linux/amd64`, push an immutable tag, then set
the image digest and unique disposable group name in `cloud.py`. Its deployment
is one RTX 3060 (12 GB), two vCPUs, 2048 MB RAM, 5 GiB storage, high priority, no
automatic restart, and a readiness probe on port 8080. Review the current quota,
GPU availability and pricing returned by `create`. Never reuse a production group.
`cloud.py` reads credentials from `.env.salad` and stores sanitized evidence under
ignored `artifacts/`. Create `artifacts/test.env` with a random `SALAD_TEST_TOKEN`
and mode 0600 before deployment. Do not commit either secret file.

```sh
python3 validation/cloud.py create
python3 validation/cloud.py status
# Use the networking.dns returned above, once the instance is ready:
python3 validation/matrix.py https://GROUP.salad.cloud artifacts/test.env artifacts/salad-matrix.json
```

The 39 scenarios cover pass, confirmed failure and unknown for all eleven checks,
disabled checks, unknown blocking, multiple failures, a custom reason, an HTML
download response, and benchmark timeout. The matrix always uses dry-run. It saves
actual observations and assertion errors after every case and exits nonzero on
any mismatch. Unsupported host telemetry is a failed validation expectation,
not a fabricated passing result. Each 4 MiB transfer uses generated data only.

The default fixture URL is the group's HTTPS gateway. A node connects out through
the gateway and back to the fixture on itself; this tests real network requests,
response validation and decisions. The resulting rates include both directions,
gateway and TLS overhead and are **not independent node uplink/downlink capacity**.
For local-only tests, pass `http://127.0.0.1:8080` as the optional fourth argument;
those rates measure loopback only. The CPU benchmark tests the executable-score
contract; supply a workload benchmark for actual inference suitability.

Once the dry-run failure is verified, a deliberate live reallocation is separate:

```sh
python3 validation/enforce.py --confirm-live-reallocation
python3 validation/verify_reallocation.py
```

The controller confirms free-VRAM failure first and creates
`artifacts/enforcement-attempt.json` **before** sending enforcement. It never
retries an uncertain POST. Expected result: IMDS 204, wrapper exit 20, application
marker absent. Then confirm the old machine has been replaced using the API and
`/info`; HTTP acceptance alone is not proof of completed reallocation. Keep the
attempt marker, including after errors, until the outcome is investigated.

On 2026-11-11 the real IMDS endpoint returned **200**, while its published schema
still required **204**. The wrapper returned exit 22 (`uncertain`) and blocked the
application. Salad system events and a different ready machine confirmed that the
request took effect. Use the read-only verifier after uncertain results too;
never rerun enforcement to obtain a preferred status code.

## Percentage scenarios

With the current validation image deployed and ready:

```sh
python3 validation/percent.py observe
# Enforce a natural shortage below 95% free VRAM:
python3 validation/percent.py vram --natural
python3 validation/percent.py verify-vram
# Enforce only if the real configured/default power ratio is below 90%:
python3 validation/percent.py power
python3 validation/percent.py verify-power
```

`observe` also tests the VRAM percentage with a temporary allocation of 10% of
GPU memory. Omitting `--natural` from `vram` uses that same allocation during
enforcement. It is released when the run finishes, fails, or the container ends.
The successful live VRAM enforcement on November 12 used `--natural`, without
an allocation fixture. The power test also failed naturally at 136/170 W (80%).

The controller sets `UNKNOWN_POLICY=block`; missing telemetry blocks the test
application without treating it as a confirmed hardware failure. Equality at
95% VRAM or 90% power passes. An actually healthy node will not be reallocated by
the corresponding scenario.

Evidence and exclusive attempt markers are stored in
`artifacts/percent-20261112/`. Run each verifier immediately after its scenario,
before further checks: it verifies a different ready machine, matching API and
container identities, and empty test history on the replacement. A lost gateway
response is possible when the node is replaced. Keep the attempt marker and use
the read-only verifier and platform logs; do not retry enforcement. To conduct a
separate test session, choose a new evidence directory explicitly rather than
removing completed or uncertain attempt markers.

See [the November 12 report](../docs/validation-2026-11-12.md) for observations,
published digests, and limits of the evidence.

## Cleanup

Stop the disposable group after collecting evidence:

```sh
python3 validation/cloud.py stop
python3 validation/cloud.py status
```

Retain the stopped group for inspection or explicitly delete it later. The helper
checks that the group still has the validation image and a single replica before
stopping it. These scripts have no automatic create, start, enforcement or cleanup
retries.
