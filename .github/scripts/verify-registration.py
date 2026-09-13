#!/usr/bin/env python3
import argparse
import ctypes
import json
from pathlib import Path

PLUGIN_ID = "opencode-session-mapper"
REPOSITORY = "https://github.com/ahoo/cpa-plugin-opencode-session-mapper"
MAX_REQUEST_PAYLOAD = 8 << 20


class Buffer(ctypes.Structure):
    _fields_ = [("ptr", ctypes.c_void_p), ("len", ctypes.c_size_t)]


def load_functions(path: Path):
    library = ctypes.CDLL(str(path.resolve()))
    call = library.cliproxyPluginCall
    call.argtypes = [
        ctypes.c_char_p,
        ctypes.POINTER(ctypes.c_uint8),
        ctypes.c_size_t,
        ctypes.POINTER(Buffer),
    ]
    call.restype = ctypes.c_int
    free = library.cliproxyPluginFree
    free.argtypes = [ctypes.c_void_p, ctypes.c_size_t]
    free.restype = None
    return library, call, free


def invoke(call, free, method: str, payload: bytes | None = None, declared_length: int | None = None):
    raw = payload or b""
    request = (ctypes.c_uint8 * len(raw)).from_buffer_copy(raw) if payload else None
    response = Buffer()
    length = len(raw) if declared_length is None else declared_length
    status = call(method.encode(), request, length, ctypes.byref(response))
    try:
        data = ctypes.string_at(response.ptr, response.len) if response.ptr else b""
    finally:
        if response.ptr:
            free(response.ptr, response.len)
    if not data:
        raise SystemExit(f"{method} returned an empty response (status={status})")
    try:
        return status, json.loads(data)
    except json.JSONDecodeError as error:
        raise SystemExit(f"{method} returned invalid JSON (status={status}): {error}") from error


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--library", required=True, type=Path)
    parser.add_argument("--version", required=True)
    args = parser.parse_args()

    _library, call, free = load_functions(args.library)
    status, envelope = invoke(call, free, "plugin.register")
    if status != 0 or not envelope.get("ok"):
        raise SystemExit(f"registration failed: status={status} envelope={envelope}")

    registration = envelope["result"]
    metadata = registration["metadata"]
    expected = {
        "Name": PLUGIN_ID,
        "Version": args.version,
        "Author": "ahoo",
        "GitHubRepository": REPOSITORY,
        "Logo": "",
        "ConfigFields": [],
    }
    if metadata != expected:
        raise SystemExit(f"registration metadata mismatch: {metadata!r}")
    if registration.get("schema_version") != 1:
        raise SystemExit(f"unexpected schema version: {registration!r}")
    if registration.get("capabilities") != {"request_interceptor": True}:
        raise SystemExit(f"unexpected capabilities: {registration!r}")

    for label, length in (("nil-buffer", 1), ("oversized", MAX_REQUEST_PAYLOAD + 1)):
        status, envelope = invoke(call, free, "request.intercept_after", declared_length=length)
        if status != 1 or envelope.get("ok") or envelope.get("error", {}).get("code") != "invalid_request":
            raise SystemExit(f"{label} guard failed: status={status} envelope={envelope}")

    print(f"verified {PLUGIN_ID} registration {args.version} and ABI guards")


if __name__ == "__main__":
    main()
