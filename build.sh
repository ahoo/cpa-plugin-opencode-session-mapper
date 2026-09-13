#!/usr/bin/env bash
# Build opencode-session-mapper for the Debian/glibc CLIProxyAPI runtime.
set -euo pipefail

SRC_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PLUGIN_NAME="opencode-session-mapper"
PLUGIN_VERSION="${PLUGIN_VERSION:-0.3.1}"
OUT_DIR="${PLUGIN_OUT_DIR:-${SRC_DIR}/dist/local/linux_amd64}"

# Pin both the Go patch release and the multi-architecture image digest so a
# release build cannot silently change underneath an existing version.
GO_IMAGE="${PLUGIN_GO_IMAGE:-golang:1.26.8-bookworm@sha256:9fdc884aacc3bec89b20ffc69f4bb369c78210e3e4f600387b5128b12c199f81}"

if [[ ! "${PLUGIN_VERSION}" =~ ^[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]]; then
  printf 'invalid plugin version: %s\n' "${PLUGIN_VERSION}" >&2
  exit 1
fi

mkdir -p "${OUT_DIR}"
OUT_DIR="$(cd "${OUT_DIR}" && pwd)"

echo "[build] source=${SRC_DIR}"
echo "[build] output=${OUT_DIR}"
echo "[build] version=${PLUGIN_VERSION}"

docker run --rm \
  --user "$(id -u):$(id -g)" \
  -v "${SRC_DIR}:/src:ro" \
  -v "${OUT_DIR}:/out" \
  -w /src \
  -e "CGO_ENABLED=1" \
  -e "GOFLAGS=-mod=readonly" \
  -e "HOME=/tmp" \
  -e "GOCACHE=/tmp/go-build" \
  -e "GOMODCACHE=/tmp/go-mod" \
  -e "PLUGIN_NAME=${PLUGIN_NAME}" \
  -e "PLUGIN_VERSION=${PLUGIN_VERSION}" \
  "${GO_IMAGE}" \
  sh -ec '
    git config --global --add safe.directory /src
    test -z "$(gofmt -l ./*.go ./.github/scripts/*.go)"
    go mod verify
    go vet ./...
    go vet ./.github/scripts
    go test ./...
    go test ./.github/scripts
    go test -race ./...
    go build -trimpath -buildvcs=true -buildmode=c-shared \
      -ldflags "-s -w -X main.pluginVersion=${PLUGIN_VERSION}" \
      -o "/out/${PLUGIN_NAME}-v${PLUGIN_VERSION}.so" .
    rm -f "/out/${PLUGIN_NAME}-v${PLUGIN_VERSION}.h"
  '

echo "[build] ok: ${OUT_DIR}/${PLUGIN_NAME}-v${PLUGIN_VERSION}.so"
