"""Hold 10% of total GPU memory for a bounded, real VRAM pressure test."""
import ctypes as c
import signal
import time

cuda = c.CDLL("libcuda.so.1")


def call(name, *args):
    status = getattr(cuda, name)(*args)
    if status:
        raise RuntimeError(f"{name} failed with status {status}")


device, context, memory = c.c_int(), c.c_void_p(), c.c_uint64()
free, total = c.c_size_t(), c.c_size_t()
call("cuInit", 0)
call("cuDeviceGet", c.byref(device), 0)
call("cuCtxCreate_v2", c.byref(context), 0, device)
def stop(*_):
    raise SystemExit(0)


signal.signal(signal.SIGTERM, stop)
try:
    call("cuMemGetInfo_v2", c.byref(free), c.byref(total))
    amount = total.value // 10
    if amount > free.value // 2:
        raise RuntimeError("Insufficient free memory for a bounded test allocation")
    call("cuMemAlloc_v2", c.byref(memory), c.c_size_t(amount))
    try:
        call("cuMemsetD8_v2", memory, 1, c.c_size_t(amount))
        print("VRAM_HELD", flush=True)
        time.sleep(90)
    finally:
        call("cuMemFree_v2", memory)
finally:
    call("cuCtxDestroy_v2", context)
