#!/usr/bin/env python3
"""A real, bounded SHA-256 throughput benchmark; score is MiB/second, CPU only."""
import hashlib
import time

data = bytes(range(256)) * 4096
start = time.monotonic()
for _ in range(128):
    hashlib.sha256(data).digest()
print(128 / (time.monotonic() - start))
