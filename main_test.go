package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func mustIntercept(t *testing.T, headers map[string][]string) interceptResponse {
	t.Helper()
	return mustInterceptWithMeta(t, headers, nil)
}

func mustInterceptWithMeta(t *testing.T, headers map[string][]string, meta map[string]any) interceptResponse {
	t.Helper()
	raw, err := json.Marshal(interceptRequest{RequestID: "test", Headers: headers, Metadata: meta})
	if err != nil {
		t.Fatal(err)
	}
	out, err := interceptHeaders(raw)
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatal(err)
	}
	if !env.OK {
		t.Fatalf("envelope not ok: %s", out)
	}
	var resp interceptResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestNilHeadersNoop(t *testing.T) {
	resp := mustIntercept(t, nil)
	if len(resp.Headers) != 0 {
		t.Fatalf("headers = %v, want empty", resp.Headers)
	}
}

func TestExistingOpencodeSessionPreserved(t *testing.T) {
	resp := mustIntercept(t, map[string][]string{
		"x-opencode-session": {"ses_client"},
		"Session-Id":         {"should-not-win"},
	})
	if len(resp.Headers) != 0 {
		t.Fatalf("headers = %v, want empty (client value authoritative)", resp.Headers)
	}
}

func TestConflictingCaseVariantSessionHeadersFailClosed(t *testing.T) {
	resp := mustInterceptWithMeta(t,
		map[string][]string{
			"Session-Id": {"session-a"},
			"session-id": {"session-b"},
		},
		map[string]any{"canonical_session_id": "codex:fallback-must-not-win"})
	if len(resp.Headers) != 0 {
		t.Fatalf("headers = %v, want empty for ambiguous session headers", resp.Headers)
	}
}

func TestRepeatedEquivalentSessionHeadersAccepted(t *testing.T) {
	resp := mustIntercept(t, map[string][]string{
		"Session-Id": {"session-a", " session-a "},
		"session-id": {"session-a"},
	})
	if got := resp.Headers["X-Opencode-Session"]; len(got) != 1 || got[0] != "session-a" {
		t.Fatalf("headers = %v, want normalized session-a", resp.Headers)
	}
}

func TestInvalidSessionHeadersFailClosed(t *testing.T) {
	invalid := []string{
		"contains\rreturn",
		"contains\nnewline",
		"contains\x00nul",
		strings.Repeat("x", maxSessionIDBytes+1),
	}
	for _, value := range invalid {
		resp := mustInterceptWithMeta(t,
			map[string][]string{"Session-Id": {value}},
			map[string]any{"canonical_session_id": "codex:fallback-must-not-win"})
		if len(resp.Headers) != 0 {
			t.Fatalf("value %q: headers = %v, want empty", value, resp.Headers)
		}
	}
}

func TestCodexSessionMapped(t *testing.T) {
	resp := mustIntercept(t, map[string][]string{"Session-Id": {"codex-ses-1"}})
	got := resp.Headers["X-Opencode-Session"]
	if len(got) != 1 || got[0] != "codex-ses-1" {
		t.Fatalf("headers = %v, want x-opencode-session=codex-ses-1", resp.Headers)
	}
	if got := resp.Headers["X-Opencode-Client"]; len(got) != 1 || got[0] != "cliproxy" {
		t.Fatalf("headers = %v, want x-opencode-client=cliproxy", resp.Headers)
	}
}

func TestUnderscoreVariantsMapped(t *testing.T) {
	for _, h := range []string{"Session_id", "Thread-Id", "Thread_id", "X-Session-Affinity", "X-Session-Id"} {
		resp := mustIntercept(t, map[string][]string{h: {"v-" + h}})
		if got := resp.Headers["X-Opencode-Session"]; len(got) != 1 || got[0] != "v-"+h {
			t.Fatalf("%s: headers = %v", h, resp.Headers)
		}
	}
}

func TestClaudeCodeSessionMapped(t *testing.T) {
	resp := mustIntercept(t, map[string][]string{"X-Claude-Code-Session-Id": {"cc-123"}})
	if got := resp.Headers["X-Opencode-Session"]; len(got) != 1 || got[0] != "cc-123" {
		t.Fatalf("headers = %v", resp.Headers)
	}
}

func TestDshSessionMapped(t *testing.T) {
	resp := mustIntercept(t, map[string][]string{"X-DeepSeek-Harness-Session-Id": {"dsh-9"}})
	if got := resp.Headers["X-Opencode-Session"]; len(got) != 1 || got[0] != "dsh-9" {
		t.Fatalf("headers = %v", resp.Headers)
	}
}

// dsh's pi-ai provider (anthropic-messages / openai-completions) sends
// x-session-affinity, unlike its native deepseek provider which sends
// x-deepseek-harness-session-id. Without this mapping those requests reach
// zen's /go/v1 without x-opencode-session and fail with MissingSessionID.
func TestDshSessionAffinityMapped(t *testing.T) {
	resp := mustIntercept(t, map[string][]string{"X-Session-Affinity": {"ses_affinity_1"}})
	if got := resp.Headers["X-Opencode-Session"]; len(got) != 1 || got[0] != "ses_affinity_1" {
		t.Fatalf("headers = %v", resp.Headers)
	}
}

func TestClientHeaderPreserved(t *testing.T) {
	resp := mustIntercept(t, map[string][]string{
		"Session-Id":        {"s1"},
		"x-opencode-client": {"opencode"},
	})
	if _, ok := resp.Headers["X-Opencode-Client"]; ok {
		t.Fatalf("headers = %v, want client identity preserved", resp.Headers)
	}
	if got := resp.Headers["X-Opencode-Session"]; len(got) != 1 || got[0] != "s1" {
		t.Fatalf("headers = %v", resp.Headers)
	}
}

// X-Client-Request-Id is a last-resort source: dsh's pi-ai openai-responses
// path stamps the session id there. It ranks below every other source, so a
// real session header always wins when both are present.
func TestClientRequestIDMappedAsLastResort(t *testing.T) {
	resp := mustIntercept(t, map[string][]string{"X-Client-Request-Id": {"req-1"}})
	if got := resp.Headers["X-Opencode-Session"]; len(got) != 1 || got[0] != "req-1" {
		t.Fatalf("headers = %v, want x-opencode-session=req-1", resp.Headers)
	}
}

func TestSessionHeaderBeatsClientRequestID(t *testing.T) {
	resp := mustIntercept(t, map[string][]string{
		"X-Client-Request-Id": {"per-call"},
		"Session-Id":          {"codex-ses-1"},
	})
	if got := resp.Headers["X-Opencode-Session"]; len(got) != 1 || got[0] != "codex-ses-1" {
		t.Fatalf("headers = %v, want the stable session id to win", resp.Headers)
	}
}

// DeepSeek Harness sends no session header at all on its pi-ai transport, so
// nothing is mappable. The host-computed canonical_session_id (present on the
// after-auth pass) supplies the conversation identity instead.
func TestCanonicalSessionFallback(t *testing.T) {
	resp := mustInterceptWithMeta(t,
		map[string][]string{"User-Agent": {"deepseek-harness/0.1.5-rc.1"}},
		map[string]any{"canonical_session_id": "codex:probe-capture-1"})
	if got := resp.Headers["X-Opencode-Session"]; len(got) != 1 || got[0] != "codex:probe-capture-1" {
		t.Fatalf("headers = %v, want fallback session id", resp.Headers)
	}
}

func TestInvalidCanonicalSessionFallbackIgnored(t *testing.T) {
	for _, value := range []string{"contains\nnewline", strings.Repeat("x", maxSessionIDBytes+1)} {
		resp := mustInterceptWithMeta(t,
			map[string][]string{"User-Agent": {"deepseek-harness/0.1.5-rc.1"}},
			map[string]any{"canonical_session_id": value})
		if len(resp.Headers) != 0 {
			t.Fatalf("value %q: headers = %v, want empty", value, resp.Headers)
		}
	}
}

// A client-supplied source must always outrank the fallback, even when both
// are present — the fallback exists only for clients that identify nothing.
func TestClientHeaderBeatsFallback(t *testing.T) {
	resp := mustInterceptWithMeta(t,
		map[string][]string{"Session-Id": {"codex-ses-1"}},
		map[string]any{"canonical_session_id": "codex:should-not-win"})
	if got := resp.Headers["X-Opencode-Session"]; len(got) != 1 || got[0] != "codex-ses-1" {
		t.Fatalf("headers = %v, want client header to win", resp.Headers)
	}
}

func TestNoFallbackWithoutMetadata(t *testing.T) {
	resp := mustIntercept(t, map[string][]string{"User-Agent": {"deepseek-harness/0.1.5-rc.1"}})
	if len(resp.Headers) != 0 {
		t.Fatalf("headers = %v, want empty when no metadata and no source header", resp.Headers)
	}
}

func TestFallbackIgnoresMalformedMetadata(t *testing.T) {
	for _, meta := range []map[string]any{
		{"canonical_session_id": ""},
		{"canonical_session_id": "   "},
		{"canonical_session_id": 42},
		{"canonical_session_id": nil},
		{"other_key": "x"},
	} {
		resp := mustInterceptWithMeta(t, map[string][]string{"User-Agent": {"dsh"}}, meta)
		if len(resp.Headers) != 0 {
			t.Fatalf("meta %v: headers = %v, want empty", meta, resp.Headers)
		}
	}
}

func TestRegister(t *testing.T) {
	out, err := handleMethod("plugin.register", nil)
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatal(err)
	}
	if !env.OK {
		t.Fatalf("register not ok: %s", out)
	}
	var reg registration
	if err := json.Unmarshal(env.Result, &reg); err != nil {
		t.Fatal(err)
	}
	if !reg.Capabilities.RequestInterceptor {
		t.Fatal("want request_interceptor capability")
	}
	if reg.Metadata.Version != pluginVersion || reg.Metadata.Author != "ahoo" || reg.Metadata.GitHubRepository != "https://github.com/ahoo/cpa-plugin-opencode-session-mapper" {
		t.Fatalf("metadata = %+v, want current release identity", reg.Metadata)
	}
}

func TestRequestPayloadSizeAllowed(t *testing.T) {
	if !requestPayloadSizeAllowed(maxRequestPayloadBytes) {
		t.Fatal("maximum request payload should be accepted")
	}
	if requestPayloadSizeAllowed(maxRequestPayloadBytes + 1) {
		t.Fatal("oversized request payload should be rejected")
	}
}
