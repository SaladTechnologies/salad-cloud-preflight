"""Brief GPU memory workload for a nonzero utilization measurement. No tuning."""
import ctypes as c
import time

cuda = c.CDLL("libcuda.so.1")


def call(name, *args):
    status = getattr(cuda, name)(*args)
    if status:
        raise RuntimeError(f"{name} failed with status {status}")


device, context, memory = c.c_int(), c.c_void_p(), c.c_uint64()
call("cuInit", 0)
call("cuDeviceGet", c.byref(device), 0)
call("cuCtxCreate_v2", c.byref(context), 0, device)
try:
    call("cuMemAlloc_v2", c.byref(memory), c.c_size_t(64 * 1024 * 1024))
    try:
        deadline = time.monotonic() + 20
        while time.monotonic() < deadline:
            call("cuMemsetD8_v2", memory, 1, c.c_size_t(64 * 1024 * 1024))
    finally:
        call("cuMemFree_v2", memory)
finally:
    call("cuCtxDestroy_v2", context)
