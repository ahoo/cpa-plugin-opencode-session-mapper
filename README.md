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
| `X-DeepSeek-Harness-Session-Id` | dsh / deepseek-harness (native provider) |
| `X-Session-Affinity` | dsh pi-ai (anthropic-messages / openai-completions) |
| `X-Session-Id` | generic / opencode-ish clients |
| `X-Client-Request-Id` | last resort (dsh pi-ai openai-responses) |

Rules:

- A client-sent `x-opencode-session` (real opencode CLI) is authoritative and
  never overridden.
- A client-supplied header always outranks the metadata fallback below; a
  request that identifies itself is never overridden.
- No header at all → the **metadata fallback** below, then no-op.
- Session identifiers are trimmed, limited to 1024 bytes, and rejected if
  they contain control characters. Conflicting repeated or case-variant
  values fail closed instead of depending on Go map iteration order.
- Interceptor payloads larger than 8 MiB are rejected before the C `size_t`
  length is converted for `C.GoBytes`.

### Metadata fallback

Some clients send no session header at all. DeepSeek Harness is the motivating
case: its pi-ai transport gates session headers behind `sendSessionAffinityHeaders`,
which the dsh adapter marks `withhold` — so nothing reaches the wire and there is
nothing to map.

For those requests the plugin reads `Metadata.canonical_session_id` from the
interceptor payload, which the host computes from the source protocol and the
client's own session id (`claude:<uuid>`, `codex:<id>`, …). It is populated only
on the **after-auth** pass, which is why it is a fallback rather than a source.

`derived_session_id` and `lcp_affinity_session_id` are deliberately **not** used:
the former is absent from this payload on v7.2.157, and core documents the latter
as unusable for provider conversation identity.

**Known limitation.** `canonical_session_id` is stateless: two independent
conversations whose content is byte-identical (same opening message, same tools)
hash to the same value, so they share one upstream session. This affects
prompt-cache affinity only, never correctness. Distinguishing such conversations
requires the client to send a real session id.

Verified live against zen:

- `muse-free` (codex `/v1/responses`, client `Session-Id`) and `mimo-free`
  (openai-compat `/v1/chat/completions`, client `X-DeepSeek-Harness-Session-Id`)
  arrive upstream carrying the mapped `X-Opencode-Session`.
- `deepseek-flash` with **no** session header at all (dsh 0.1.5,
  `/v1/chat/completions`) now returns 200 instead of upstream
  `MissingSessionID` 400; the derived id stays stable as a conversation grows
  turn by turn and differs between conversations.

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
(`libc.musl-x86_64.so.1: cannot open shared object file`). The build script
pins the Go patch release and container image digest, uses module read-only
mode, and embeds VCS provenance. Use it from a clean Git checkout:

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
4. `gh release create v0.3.0 opencode-session-mapper_0.3.0_linux_amd64.zip checksums.txt`
5. Bump `version` in `registry.json`.

## License

MIT
