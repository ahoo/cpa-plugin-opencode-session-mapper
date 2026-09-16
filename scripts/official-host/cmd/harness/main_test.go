// SPDX-License-Identifier: MIT
package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMakeLargeRequestBody(t *testing.T) {
	body, err := makeLargeRequestBody()
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != requestBodyBytes {
		t.Fatalf("body length = %d, want %d", len(body), requestBodyBytes)
	}
	if !json.Valid(body) {
		t.Fatal("body is not valid JSON")
	}
	if !strings.Contains(string(body[:256]), syntheticMarker) {
		t.Fatal("body marker is absent")
	}
}

func TestPluginReady(t *testing.T) {
	ready := pluginListResponse{
		PluginsEnabled: true,
		Plugins: []pluginListEntry{{
			ID:               pluginID,
			Configured:       true,
			Registered:       true,
			Enabled:          true,
			EffectiveEnabled: true,
		}},
	}
	if !pluginReady(ready) {
		t.Fatal("exact plugin state is not ready")
	}
	mutations := []func(*pluginListResponse){
		func(value *pluginListResponse) { value.PluginsEnabled = false },
		func(value *pluginListResponse) { value.Plugins[0].Configured = false },
		func(value *pluginListResponse) { value.Plugins[0].Registered = false },
		func(value *pluginListResponse) { value.Plugins[0].Enabled = false },
		func(value *pluginListResponse) { value.Plugins[0].EffectiveEnabled = false },
		func(value *pluginListResponse) { value.Plugins[0].ID = "other" },
	}
	for index, mutate := range mutations {
		changed := ready
		changed.Plugins = append([]pluginListEntry(nil), ready.Plugins...)
		mutate(&changed)
		if pluginReady(changed) {
			t.Fatalf("invalid plugin state accepted index=%d", index)
		}
	}
	if pluginReady(pluginListResponse{PluginsEnabled: true}) {
		t.Fatal("missing plugin accepted")
	}
}

func TestIdentifierValidators(t *testing.T) {
	if !isSHA256(strings.Repeat("a", 64)) || isSHA256(strings.Repeat("a", 63)) || isSHA256(strings.Repeat("g", 64)) {
		t.Fatal("SHA-256 validator mismatch")
	}
	if !isImageDigest("sha256:"+strings.Repeat("a", 64)) || isImageDigest("sha256:"+strings.Repeat("a", 63)) || isImageDigest("sha512:"+strings.Repeat("a", 64)) {
		t.Fatal("image digest validator mismatch")
	}
	if !isRevision(strings.Repeat("b", 40)) || isRevision(strings.Repeat("b", 39)) || isRevision(strings.Repeat("G", 40)) {
		t.Fatal("revision validator mismatch")
	}
}

func TestDecodePluginListPreservesFullPinnedCommit(t *testing.T) {
	const fullCommit = "8335eac731946bd4eff18f500653f93736df53d6"
	response := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"plugins_enabled":true,"plugins":[]}`)),
	}
	response.Header.Set("X-CPA-VERSION", "v7.3.4")
	response.Header.Set("X-CPA-COMMIT", fullCommit[:7])
	response.Header.Set("X-CPA-BUILD-DATE", expectedHostBuild)
	opts := verifyOptions{
		hostVersion:   "v7.3.4",
		hostCommit:    fullCommit,
		hostBuildDate: expectedHostBuild,
		imageRepo:     "eceasy/cli-proxy-api",
		imageDigest:   "sha256:" + strings.Repeat("a", 64),
	}
	_, identity, err := decodePluginList(response, opts)
	if err != nil {
		t.Fatal(err)
	}
	if identity.Commit != fullCommit {
		t.Fatalf("reported commit = %q", identity.Commit)
	}

	response = &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"plugins_enabled":true,"plugins":[]}`)),
	}
	opts.hostCommit = fullCommit[:7]
	if _, _, err = decodePluginList(response, opts); err == nil {
		t.Fatal("short commit pin was accepted")
	}
}

func TestWriteJSONExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	value := phaseReport{SchemaVersion: 1, SourceRevision: strings.Repeat("a", 40)}
	if err := writeJSONExclusive(path, value); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("report mode = %#o", info.Mode().Perm())
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = writeJSONExclusive(path, value); err == nil {
		t.Fatal("existing report was replaced")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("existing report changed")
	}
}
