#!/usr/bin/env python3
"""Verify registration and bounded native ABI behavior for a built plugin.

Diagnostics are value-free: failures report only fixed labels, status codes,
envelope flags/codes, and byte lengths. They never include payloads, header
values, session identifiers, or raw plugin responses.
"""

import argparse
import base64
import ctypes
import json
from pathlib import Path

PLUGIN_ID = "opencode-session-mapper"
REPOSITORY = "https://github.com/ahoo/cpa-plugin-opencode-session-mapper"
MAX_REQUEST_PAYLOAD = 64 << 20
LARGE_BODY_BYTES = 6_378_698
SIZE_MAX = (1 << (8 * ctypes.sizeof(ctypes.c_size_t))) - 1
SESSION_FIXTURE = "123e4567-e89b-12d3-a456-426614174000"


class Buffer(ctypes.Structure):
    _fields_ = [("ptr", ctypes.c_void_p), ("len", ctypes.c_size_t)]


class PluginAPI(ctypes.Structure):
    _fields_ = [
        ("abi_version", ctypes.c_uint32),
        ("call", ctypes.c_void_p),
        ("free_buffer", ctypes.c_void_p),
        ("shutdown", ctypes.c_void_p),
    ]


def load_functions(path: Path):
    library = ctypes.CDLL(str(path.resolve()))
    init = library.cliproxy_plugin_init
    init.argtypes = [ctypes.c_void_p, ctypes.POINTER(PluginAPI)]
    init.restype = ctypes.c_int
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
    shutdown = library.cliproxyPluginShutdown
    shutdown.argtypes = []
    shutdown.restype = None
    return library, init, call, free, shutdown


def invoke(
    call,
    free,
    method: str | None,
    payload: bytes | None = None,
    declared_length: int | None = None,
):
    raw = payload or b""
    request = (ctypes.c_uint8 * len(raw)).from_buffer_copy(raw) if raw else None
    response = Buffer()
    length = len(raw) if declared_length is None else declared_length
    method_raw = method.encode() if method is not None else None
    status = call(method_raw, request, length, ctypes.byref(response))
    try:
        data = ctypes.string_at(response.ptr, response.len) if response.ptr else b""
    finally:
        if response.ptr:
            free(response.ptr, response.len)
    label = method or "nil-method"
    if not data:
        raise SystemExit(f"{label} returned an empty response: status={status}")
    try:
        return status, json.loads(data)
    except (json.JSONDecodeError, UnicodeDecodeError) as error:
        raise SystemExit(f"{label} returned invalid JSON: status={status} bytes={len(data)}") from error


def error_code(envelope) -> str | None:
    if not isinstance(envelope, dict):
        return None
    error = envelope.get("error")
    return error.get("code") if isinstance(error, dict) else None


def require_error(label: str, status: int, envelope, code: str) -> None:
    ok = envelope.get("ok") if isinstance(envelope, dict) else None
    actual = error_code(envelope)
    if status != 1 or ok is not False or actual != code:
        raise SystemExit(
            f"{label} guard failed: status={status} ok={ok} code={actual}"
        )


def require_mapping(label: str, status: int, envelope) -> None:
    if status != 0 or not isinstance(envelope, dict) or envelope.get("ok") is not True:
        ok = envelope.get("ok") if isinstance(envelope, dict) else None
        raise SystemExit(f"{label} mapping failed: status={status} ok={ok}")
    result = envelope.get("result")
    headers = result.get("Headers") if isinstance(result, dict) else None
    if not isinstance(headers, dict):
        raise SystemExit(f"{label} mapping failed: result_shape=false")
    session = headers.get("X-Opencode-Session")
    client = headers.get("X-Opencode-Client")
    if session != [SESSION_FIXTURE] or client != ["cliproxy"]:
        raise SystemExit(f"{label} mapping failed: headers_exact=false")


def host_shaped_payload() -> bytes:
    body = base64.b64encode(b"a" * LARGE_BODY_BYTES).decode("ascii")
    payload = json.dumps(
        {
            "RequestID": "large-envelope",
            "TraceID": "trace",
            "SourceFormat": "claude",
            "ToFormat": "openai",
            "Model": "mock-model",
            "RequestedModel": "mock-model",
            "Stream": False,
            "Headers": {"X-Claude-Code-Session-Id": [SESSION_FIXTURE]},
            "Body": body,
            "Metadata": {},
            "HostCallbackID": "callback",
        },
        separators=(",", ":"),
    ).encode()
    if len(payload) <= 8 << 20 or len(payload) > MAX_REQUEST_PAYLOAD:
        raise SystemExit(f"large-envelope fixture size invalid: bytes={len(payload)}")
    return payload


def exact_cap_payload() -> bytes:
    prefix = (
        b'{"RequestID":"cap","Headers":{"X-Claude-Code-Session-Id":["'
        + SESSION_FIXTURE.encode()
        + b'"]},"Padding":"'
    )
    suffix = b'"}'
    padding = MAX_REQUEST_PAYLOAD - len(prefix) - len(suffix)
    if padding <= 0:
        raise SystemExit("exact-cap fixture construction failed")
    payload = prefix + (b"a" * padding) + suffix
    if len(payload) != MAX_REQUEST_PAYLOAD:
        raise SystemExit(f"exact-cap fixture size invalid: bytes={len(payload)}")
    return payload


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--library", required=True, type=Path)
    parser.add_argument("--version", required=True)
    args = parser.parse_args()

    _library, init, call, free, shutdown = load_functions(args.library)

    if init(None, None) != 1:
        raise SystemExit("nil plugin API guard failed")
    plugin = PluginAPI()
    if init(None, ctypes.byref(plugin)) != 0:
        raise SystemExit("plugin init failed")
    if plugin.abi_version != 1:
        raise SystemExit(f"plugin ABI mismatch: abi={plugin.abi_version}")
    if not plugin.call or not plugin.free_buffer or not plugin.shutdown:
        raise SystemExit("plugin API function pointers are incomplete")

    status, envelope = invoke(call, free, "plugin.register")
    if status != 0 or not isinstance(envelope, dict) or envelope.get("ok") is not True:
        ok = envelope.get("ok") if isinstance(envelope, dict) else None
        raise SystemExit(f"registration failed: status={status} ok={ok}")
    registration = envelope.get("result")
    if not isinstance(registration, dict):
        raise SystemExit("registration result shape mismatch")
    expected_metadata = {
        "Name": PLUGIN_ID,
        "Version": args.version,
        "Author": "ahoo",
        "GitHubRepository": REPOSITORY,
        "Logo": "",
        "ConfigFields": [],
    }
    if registration.get("metadata") != expected_metadata:
        raise SystemExit("registration metadata mismatch")
    if registration.get("schema_version") != 1:
        raise SystemExit("registration schema mismatch")
    if registration.get("capabilities") != {"request_interceptor": True}:
        raise SystemExit("registration capabilities mismatch")

    large_payload = host_shaped_payload()
    for method in ("request.intercept_before", "request.intercept_after"):
        status, envelope = invoke(call, free, method, payload=large_payload)
        require_mapping(f"{method}:large-envelope", status, envelope)

    cap_payload = exact_cap_payload()
    for method in ("request.intercept_before", "request.intercept_after"):
        status, envelope = invoke(call, free, method, payload=cap_payload)
        require_mapping(f"{method}:exact-cap", status, envelope)

    for label, length in (
        ("cap-plus-one", MAX_REQUEST_PAYLOAD + 1),
        ("size-max", SIZE_MAX),
    ):
        status, envelope = invoke(
            call, free, "request.intercept_after", declared_length=length
        )
        require_error(label, status, envelope, "invalid_request")

    status, envelope = invoke(
        call, free, "request.intercept_after", declared_length=1
    )
    require_error("nil-buffer", status, envelope, "invalid_request")

    status, envelope = invoke(call, free, None)
    require_error("nil-method", status, envelope, "invalid_method")

    nil_response_status = call(b"plugin.register", None, 0, None)
    if nil_response_status != 1:
        raise SystemExit(f"nil-response guard failed: status={nil_response_status}")

    for attempt in range(2):
        status, envelope = invoke(
            call, free, "request.intercept_after", payload=b'{"Headers":'
        )
        require_error(f"malformed-json-{attempt + 1}", status, envelope, "plugin_error")

    # Freeing a nil pointer is an explicit no-op. Normal response ownership is
    # exercised by every invoke call above.
    free(None, 0)

    status, envelope = invoke(call, free, "request.intercept_after", payload=large_payload)
    require_mapping("repeated-call", status, envelope)
    shutdown()

    print(
        f"verified {PLUGIN_ID} registration {args.version}, ABI init/free guards, "
        f"both interceptor methods, large-envelope mapping, and {MAX_REQUEST_PAYLOAD}-byte cap"
    )


if __name__ == "__main__":
    main()
