#!/usr/bin/env bash
# SPDX-License-Identifier: MIT
set -euo pipefail

readonly HOST_IMAGE_REPOSITORY='eceasy/cli-proxy-api'
readonly HOST_IMAGE_DIGEST='sha256:97825da3009f98acf78b5c172fde650a5fbe7a690950a69ce6d7b535d77d4266'
readonly HOST_IMAGE="${HOST_IMAGE_REPOSITORY}@${HOST_IMAGE_DIGEST}"
readonly HOST_VERSION='v7.3.4'
readonly HOST_COMMIT='8335eac731946bd4eff18f500653f93736df53d6'
readonly HOST_BUILD_DATE='2026-09-15T14:07:07Z'
readonly MANAGEMENT_KEY='mapper-harness-management'
readonly CLIENT_KEY='mapper-harness-client'
readonly BODY_MARKER='mapper-large-envelope-marker-q7z'
readonly SESSION_MARKER='123e4567-e89b-12d3-a456-426614174000'

library=''
version=''
revision=''
output=''
while (($# > 0)); do
  case "$1" in
    --library)
      library="${2:-}"
      shift 2
      ;;
    --version)
      version="${2:-}"
      shift 2
      ;;
    --revision)
      revision="${2:-}"
      shift 2
      ;;
    --output)
      output="${2:-}"
      shift 2
      ;;
    *)
      printf 'unknown official-Host harness argument\n' >&2
      exit 2
      ;;
  esac
done

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
if [[ -z "$library" || -z "$version" || -z "$revision" || -z "$output" ]]; then
  printf 'library, version, revision, and output are required\n' >&2
  exit 2
fi
library="$(realpath "$library")"
output="$(realpath -m "$output")"
if [[ ! -f "$library" || ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-dev(\.[0-9a-f]{12})?)?$ || ! "$revision" =~ ^[0-9a-f]{40}$ ]]; then
  printf 'invalid official-Host harness input\n' >&2
  exit 2
fi
case "$library" in
  "$repo_root"/dist/*|/home/ubuntu/workspace/cliproxyapi/*)
    printf 'refusing a forbidden plugin artifact path\n' >&2
    exit 2
    ;;
esac
if [[ -e "$output" ]]; then
  printf 'refusing to replace an existing integration report\n' >&2
  exit 2
fi
mkdir -p "$(dirname "$output")"

for command in docker go python3 sha256sum readelf realpath timeout; do
  command -v "$command" >/dev/null || {
    printf 'required official-Host harness command is unavailable\n' >&2
    exit 1
  }
done
if [[ "$(uname -m)" != 'x86_64' ]]; then
  printf 'official-Host harness requires Linux amd64\n' >&2
  exit 1
fi
readelf -h "$library" | grep -F 'Machine:' | grep -F 'Advanced Micro Devices X86-64' >/dev/null
readelf -d "$library" | grep -F 'Shared library: [libc.so.6]' >/dev/null

work_root="$(mktemp -d "${TMPDIR:-/tmp}/mapper-official-host.XXXXXXXX")"
prefix="mapper-host-${work_root##*.}"
network="${prefix}-network"
host_container="${prefix}-host"
mock_container="${prefix}-mock"
verifier_container="${prefix}-verifier"

cleanup() {
  docker rm -f "$host_container" "$mock_container" "$verifier_container" >/dev/null 2>&1 || true
  docker network rm "$network" >/dev/null 2>&1 || true
  rm -rf "$work_root"
}
trap cleanup EXIT INT TERM

mkdir -p \
  "$work_root/bin" \
  "$work_root/config" \
  "$work_root/auth" \
  "$work_root/logs" \
  "$work_root/plugins/linux/amd64" \
  "$work_root/reports"

candidate_name="opencode-session-mapper-v${version}.so"
install -m 0755 "$library" "$work_root/plugins/linux/amd64/$candidate_name"

go -C "$repo_root" test ./scripts/official-host/cmd/harness
CGO_ENABLED=0 go -C "$repo_root" build -trimpath \
  -o "$work_root/bin/official-host-harness" ./scripts/official-host/cmd/harness
plugin_sha256="$(sha256sum "$library" | cut -d' ' -f1)"
python3 "$repo_root/.github/scripts/verify-registration.py" \
  --library "$library" --version "$version" >/dev/null

cat >"$work_root/config/config.yaml" <<'YAML'
host: "0.0.0.0"
port: 8317
tls:
  enable: false
remote-management:
  allow-remote: true
  secret-key: "mapper-harness-management"
  disable-control-panel: true
  disable-auto-update-panel: true
auth-dir: "/harness/auth"
api-keys:
  - "mapper-harness-client"
debug: false
pprof:
  enable: false
logging-to-file: false
request-log: false
usage-statistics-enabled: false
error-logs-max-files: 2
request-retry: 0
plugins:
  enabled: true
  dir: "/harness/plugins"
  configs:
    opencode-session-mapper:
      enabled: true
      priority: 1
openai-compatibility:
  - name: "mapper-harness"
    base-url: "http://mock:9000/v1"
    api-key-entries:
      - api-key: "mapper-harness-upstream"
    headers:
      X-Opencode-Session: $X-Opencode-Session
      X-Opencode-Client: $X-Opencode-Client
    models:
      - name: "mock-model"
        alias: "mock-model"
YAML

docker pull "$HOST_IMAGE" >/dev/null
repo_digests="$(docker image inspect --format '{{join .RepoDigests "\n"}}' "$HOST_IMAGE")"
grep -Fx "${HOST_IMAGE_REPOSITORY}@${HOST_IMAGE_DIGEST}" <<<"$repo_digests" >/dev/null

docker network create --internal "$network" >/dev/null

docker run -d \
  --name "$mock_container" \
  --network "$network" \
  --network-alias mock \
  --user "$(id -u):$(id -g)" \
  --read-only \
  --cap-drop ALL \
  --security-opt no-new-privileges \
  --pids-limit 64 \
  --memory 256m \
  --cpus 1 \
  --tmpfs /tmp:rw,nosuid,nodev,noexec,size=16m \
  --mount "type=bind,src=$work_root/bin/official-host-harness,dst=/harness/bin,readonly" \
  --entrypoint /harness/bin \
  "$HOST_IMAGE" --mode mock --listen :9000 >/dev/null

docker run -d \
  --name "$host_container" \
  --network "$network" \
  --network-alias host \
  --read-only \
  --cap-drop ALL \
  --security-opt no-new-privileges \
  --pids-limit 256 \
  --memory 1g \
  --cpus 2 \
  --tmpfs /tmp:rw,nosuid,nodev,noexec,size=128m \
  --tmpfs /root/.cache:rw,nosuid,nodev,noexec,size=16m \
  --mount "type=bind,src=$work_root/config,dst=/harness/config,readonly" \
  --mount "type=bind,src=$work_root/auth,dst=/harness/auth" \
  --mount "type=bind,src=$work_root/logs,dst=/CLIProxyAPI/logs" \
  --mount "type=bind,src=$work_root/plugins,dst=/harness/plugins,readonly" \
  "$HOST_IMAGE" ./CLIProxyAPI --config /harness/config/config.yaml >/dev/null

if ! timeout --signal=TERM --kill-after=10s 180s docker run --rm \
  --name "$verifier_container" \
  --network "$network" \
  --user "$(id -u):$(id -g)" \
  --read-only \
  --cap-drop ALL \
  --security-opt no-new-privileges \
  --pids-limit 64 \
  --memory 256m \
  --cpus 1 \
  --tmpfs /tmp:rw,nosuid,nodev,noexec,size=16m \
  --mount "type=bind,src=$work_root/bin/official-host-harness,dst=/harness/bin,readonly" \
  --mount "type=bind,src=$work_root/reports,dst=/harness/reports" \
  --entrypoint /harness/bin \
  "$HOST_IMAGE" \
    --mode verify \
    --host-url http://host:8317 \
    --mock-url http://mock:9000 \
    --management-key "$MANAGEMENT_KEY" \
    --api-key "$CLIENT_KEY" \
    --plugin-version "$version" \
    --plugin-sha256 "$plugin_sha256" \
    --source-revision "$revision" \
    --image-repository "$HOST_IMAGE_REPOSITORY" \
    --image-digest "$HOST_IMAGE_DIGEST" \
    --host-version "$HOST_VERSION" \
    --host-commit "$HOST_COMMIT" \
    --host-build-date "$HOST_BUILD_DATE" \
    --output /harness/reports/phase.json; then
  docker rm -f "$verifier_container" >/dev/null 2>&1 || true
  docker logs "$host_container" >"$work_root/host.stdout.log" 2>&1 || true
  registered_count="$(grep -a -c -F 'pluginhost: plugin registered' "$work_root/host.stdout.log" || true)"
  interceptor_error_count="$(grep -a -c -F 'pluginhost: request interceptor' "$work_root/host.stdout.log" || true)"
  panic_count="$(grep -a -c -F 'pluginhost: plugin panic recovered' "$work_root/host.stdout.log" || true)"
  printf 'sanitized Host diagnostics: registered=%s interceptor_errors=%s recovered_panics=%s\n' \
    "$registered_count" "$interceptor_error_count" "$panic_count" >&2
  exit 1
fi

if ! docker stop --time 10 "$host_container" >/dev/null; then
  printf 'official Host did not stop cleanly\n' >&2
  exit 1
fi
docker logs "$host_container" >"$work_root/host.stdout.log" 2>&1
if [[ ! -s "$work_root/host.stdout.log" ]]; then
  printf 'official Host stdout log is empty\n' >&2
  exit 1
fi
docker rm "$host_container" >/dev/null
host_container="${prefix}-removed"

interceptor_error_count="$(grep -a -c -F 'pluginhost: request interceptor' "$work_root/host.stdout.log" || true)"
panic_count="$(grep -a -c -F 'pluginhost: plugin panic recovered' "$work_root/host.stdout.log" || true)"
if [[ "$interceptor_error_count" != 0 || "$panic_count" != 0 ]]; then
  printf 'official Host emitted mapper interceptor errors or recovered panics\n' >&2
  exit 1
fi
for marker in "$BODY_MARKER" "$SESSION_MARKER"; do
  if grep -aR -F -q -- "$marker" "$work_root/logs" "$work_root/host.stdout.log"; then
    printf 'synthetic marker appeared in isolated Host logs\n' >&2
    exit 1
  fi
done

PHASE_REPORT="$work_root/reports/phase.json" \
OUTPUT_REPORT="$output" \
python3 - <<'PY'
import json
import os
from pathlib import Path

phase_path = Path(os.environ["PHASE_REPORT"])
try:
    phase = json.loads(phase_path.read_text(encoding="utf-8"))
except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
    raise SystemExit("phase report is missing or malformed") from error
expected_top = {"schema_version", "source_revision", "official_host", "plugin", "assertions"}
if set(phase) != expected_top or phase.get("schema_version") != 1:
    raise SystemExit("phase report shape mismatch")
expected_phase_assertions = {
    "large_envelope_forwarded",
    "session_header_mapped",
    "client_header_mapped",
    "mock_request_count_one",
}
assertions = phase.get("assertions")
if not isinstance(assertions, dict) or set(assertions) != expected_phase_assertions:
    raise SystemExit("phase assertion shape mismatch")
if any(value is not True for value in assertions.values()):
    raise SystemExit("phase assertion failed")
assertions.update({
    "interceptor_errors_zero": True,
    "recovered_panics_zero": True,
    "synthetic_marker_absent_from_host_logs": True,
})
phase["assertions"] = assertions
output = Path(os.environ["OUTPUT_REPORT"])
try:
    with output.open("xb") as stream:
        stream.write((json.dumps(phase, indent=2, sort_keys=True) + "\n").encode())
except FileExistsError as error:
    raise SystemExit("refusing to replace integration report") from error
PY

printf 'official Host integration passed: image=%s plugin_sha256=%s large_envelope_forwarded=true mapped_headers=true interceptor_errors=0 recovered_panics=0 log_marker_absent=true\n' \
  "$HOST_VERSION" "$plugin_sha256"
