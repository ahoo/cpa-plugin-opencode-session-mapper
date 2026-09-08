# cpa-plugin-opencode-session-mapper

CLIProxyAPI request interceptor plugin: forwards client conversation session
ids to OpenCode Zen as `x-opencode-session`.

OpenCode Zen uses `x-opencode-session` (one stable ID per conversation) for
sticky routing and prompt-cache affinity. Since 09/06, requests missing this
header may error. But requests that pass through CLIProxyAPI lose the header:
executors rebuild outbound headers from scratch and only forward a small
whitelist (`Version` / `Session_id` / `User-Agent` on the codex path, nothing
extra on the openai-compat path).

This plugin restores the session signal in two stages:

1. **Plugin** (`request.intercept_before` + `request.intercept_after`):
   reads the downstream client's own stable session headers and injects them
   as `X-Opencode-Session` into the execution headers, plus
   `X-Opencode-Client: cliproxy` when the client didn't identify itself.
2. **Config** (`headers:` with `$` references on each zen credential):
   resolves the injected values onto the actual upstream wire request via
   `X-Opencode-Session: $X-Opencode-Session`.

Session source priority (first non-empty wins):

| Header | Client |
|---|---|
| `Session-Id` / `Session_id` | codex CLI |
| `Thread-Id` / `Thread_id` | codex CLI thread (= conversation) |
| `X-Claude-Code-Session-Id` | claude code |
| `X-DeepSeek-Harness-Session-Id` | dsh / deepseek-harness |
| `X-Session-Id` | generic / opencode-ish clients |

Rules:

- A client-sent `x-opencode-session` (real opencode CLI) is authoritative and
  never overridden.
- Per-call IDs (`X-Client-Request-Id`, the `x-opencode-request` analogue) are
  deliberately not mapped — using them as the session would break stickiness.
- No header at all → no-op. There is no global static fallback: collapsing
  every conversation into one session would defeat per-conversation routing.

Verified live against zen: `muse-free` (codex `/v1/responses` path, client
`Session-Id`) and `mimo-free` (openai-compat `/v1/chat/completions` path,
client `X-DeepSeek-Harness-Session-Id`) both arrive upstream carrying the
mapped `X-Opencode-Session`.

## Capability

- `request_interceptor` — runs before and after credential selection.

## Install

Add the store source and enable the plugin in `config.yaml`:

```yaml
plugins:
  enabled: true
  store-sources:
    - https://raw.githubusercontent.com/ahoo/cpa-plugin-opencode-session-mapper/main/registry.json
  configs:
    opencode-session-mapper:
      enabled: true
      priority: 1
```

Then add `$` forwarding headers to every zen credential so the injected
values reach the wire. On each `codex-api-key` entry whose `base-url`
contains `opencode.ai`, and on the `openai-compatibility` provider pointing
at `https://opencode.ai/zen/v1`:

```yaml
headers:
  X-Opencode-Session: $X-Opencode-Session
  X-Opencode-Client: $X-Opencode-Client
```

`$Name` copies the value from the (plugin-augmented) execution headers; when
absent the header is omitted. Restart CLIProxyAPI (plugins load at startup):

```bash
docker restart cli-proxy-api
```

Verify:

```bash
docker logs cli-proxy-api | grep opencode-session-mapper
```

## Build

The CLIProxyAPI runtime image is Debian-based (glibc). Building with the
alpine Go image produces a musl-linked `.so` that fails to `dlopen` at runtime
(`libc.musl-x86_64.so.1: cannot open shared object file`). Use a glibc image:

```bash
./build.sh
```

## Test

```bash
go vet . && go test ./...
```

## Release

1. Build the `.so`.
2. Package `<id>_<version>_<goos>_<goarch>.zip` with the library at the zip
   root named `opencode-session-mapper.so`.
3. Generate `checksums.txt` (sha256 of the zip).
4. `gh release create v0.1.0 opencode-session-mapper_0.1.0_linux_amd64.zip checksums.txt`
5. Bump `version` in `registry.json`.

## License

MIT
