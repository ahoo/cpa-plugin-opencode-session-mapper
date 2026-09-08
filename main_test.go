package main

import (
	"encoding/json"
	"testing"
)

func mustIntercept(t *testing.T, headers map[string][]string) interceptResponse {
	t.Helper()
	raw, err := json.Marshal(interceptRequest{RequestID: "test", Headers: headers})
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
	for _, h := range []string{"Session_id", "Thread-Id", "Thread_id", "X-Session-Id"} {
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

func TestClientHeaderPreserved(t *testing.T) {
	resp := mustIntercept(t, map[string][]string{
		"Session-Id":      {"s1"},
		"x-opencode-client": {"opencode"},
	})
	if _, ok := resp.Headers["X-Opencode-Client"]; ok {
		t.Fatalf("headers = %v, want client identity preserved", resp.Headers)
	}
	if got := resp.Headers["X-Opencode-Session"]; len(got) != 1 || got[0] != "s1" {
		t.Fatalf("headers = %v", resp.Headers)
	}
}

func TestPerCallIDNotMapped(t *testing.T) {
	resp := mustIntercept(t, map[string][]string{"X-Client-Request-Id": {"req-1"}})
	if len(resp.Headers) != 0 {
		t.Fatalf("headers = %v, want empty (per-call id is not a session)", resp.Headers)
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
}
