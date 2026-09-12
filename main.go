package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);

static const cliproxy_host_api* stored_host;

static void store_host_api(const cliproxy_host_api* host) {
	stored_host = host;
}
*/
import "C"

import (
	"encoding/json"
	"strings"
	"unsafe"
)

const abiVersion uint32 = 1

const (
	pluginID      = "opencode-session-mapper"
	pluginVersion = "0.3.0"

	// Interceptor payloads contain request metadata, not request bodies. Keep
	// the C size_t -> Go int conversion bounded and reject unreasonable input
	// before allocating through C.GoBytes.
	maxRequestPayloadBytes = 8 << 20
	maxSessionIDBytes      = 1024

	targetSessionHeader = "X-Opencode-Session"
	targetClientHeader  = "X-Opencode-Client"
	defaultClientValue  = "cliproxy"
)

// sessionSources lists downstream client headers that carry a stable
// per-conversation session id, in priority order. The first non-empty
// value wins and is forwarded as x-opencode-session (OpenCode Zen uses
// it for sticky routing / prompt-cache affinity; since 09/06 requests
// without it may error).
//
// Deliberately EXCLUDED:
//   - Conversation_id: set by the server itself, not the client.
var sessionSources = []string{
	"Session-Id",                    // codex CLI (also matches session_id case-insensitively? no: see below)
	"Session_id",                    // codex CLI underscore variant (EqualFold misses _ vs -)
	"Thread-Id",                     // codex CLI thread = conversation
	"Thread_id",                     // underscore variant
	"X-Claude-Code-Session-Id",      // claude code
	"X-DeepSeek-Harness-Session-Id", // dsh / deepseek-harness native provider
	"X-Session-Affinity",            // dsh pi-ai (anthropic-messages, openai-completions)
	"X-Session-Id",                  // generic (opencode-ish clients, aicoding-proxy default)
	// Last resort: dsh's pi-ai openai-responses path stamps the session id here
	// (pi-ai sends it alongside x-session-affinity/x-session-id, which those
	// clients may not use). Ranked last because other tools set it per call.
	"X-Client-Request-Id",
}

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type registration struct {
	SchemaVersion uint32       `json:"schema_version"`
	Metadata      metadata     `json:"metadata"`
	Capabilities  capabilities `json:"capabilities"`
}

type metadata struct {
	Name             string `json:"Name"`
	Version          string `json:"Version"`
	Author           string `json:"Author"`
	GitHubRepository string `json:"GitHubRepository"`
	Logo             string `json:"Logo"`
	ConfigFields     []any  `json:"ConfigFields"`
}

type capabilities struct {
	RequestInterceptor bool `json:"request_interceptor"`
}

type interceptRequest struct {
	RequestID string              `json:"RequestID"`
	Headers   map[string][]string `json:"Headers"`
	// Metadata carries host-computed request facts. Populated only on the
	// after-auth pass (request.intercept_after), where it includes
	// canonical_session_id — see sessionFallback.
	Metadata map[string]any `json:"Metadata"`
}

type interceptResponse struct {
	Headers      map[string][]string `json:"Headers"`
	ClearHeaders []string            `json:"ClearHeaders"`
}

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	C.store_host_api(host)
	plugin.abi_version = C.uint32_t(abiVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, errorEnvelope("invalid_method", "method is required"))
		return 1
	}
	var payload []byte
	if !requestPayloadSizeAllowed(uint64(requestLen)) {
		writeResponse(response, errorEnvelope("invalid_request", "request payload exceeds size limit"))
		return 1
	}
	if requestLen > 0 {
		if request == nil {
			writeResponse(response, errorEnvelope("invalid_request", "request buffer is required"))
			return 1
		}
		payload = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, errHandle := handleMethod(C.GoString(method), payload)
	if errHandle != nil {
		writeResponse(response, errorEnvelope("plugin_error", errHandle.Error()))
		return 1
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, len C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
	_ = len
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {}

func handleMethod(method string, payload []byte) ([]byte, error) {
	switch method {
	case "plugin.register", "plugin.reconfigure":
		return okEnvelopeJSON(registration{
			SchemaVersion: abiVersion,
			Metadata: metadata{
				Name:             pluginID,
				Version:          pluginVersion,
				Author:           "ahoo",
				GitHubRepository: "https://github.com/ahoo/cpa-plugin-opencode-session-mapper",
				Logo:             "",
				ConfigFields:     []any{},
			},
			Capabilities: capabilities{RequestInterceptor: true},
		})
	case "request.intercept_before", "request.intercept_after":
		return interceptHeaders(payload)
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func requestPayloadSizeAllowed(size uint64) bool {
	return size <= maxRequestPayloadBytes
}

// headerValue returns a normalized session identifier and whether the header
// name was present. Empty values, invalid values, or conflicting repeated /
// case-variant values are reported as present with an empty value so callers
// can fail closed instead of selecting a value using randomized map order.
func headerValue(headers map[string][]string, name string) (string, bool) {
	matched := false
	selected := ""
	for key, values := range headers {
		if !strings.EqualFold(key, name) {
			continue
		}
		matched = true
		if len(values) == 0 {
			return "", true
		}
		for _, raw := range values {
			value, valid := normalizeSessionID(raw)
			if !valid {
				return "", true
			}
			if selected == "" {
				selected = value
				continue
			}
			if selected != value {
				return "", true
			}
		}
	}
	if !matched {
		return "", false
	}
	return selected, true
}

func headerExists(headers map[string][]string, name string) bool {
	for key := range headers {
		if strings.EqualFold(key, name) {
			return true
		}
	}
	return false
}

func normalizeSessionID(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxSessionIDBytes {
		return "", false
	}
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return "", false
		}
	}
	return value, true
}

func interceptHeaders(payload []byte) ([]byte, error) {
	empty, errEmpty := okEnvelopeJSON(interceptResponse{Headers: map[string][]string{}})
	if errEmpty != nil {
		return nil, errEmpty
	}
	var req interceptRequest
	if len(payload) > 0 {
		if errDecode := json.Unmarshal(payload, &req); errDecode != nil {
			return nil, errDecode
		}
	}
	if req.Headers == nil {
		return empty, nil
	}
	// Client already sent x-opencode-session (e.g. real opencode CLI):
	// never override, their value is authoritative.
	if _, exists := headerValue(req.Headers, targetSessionHeader); exists {
		return empty, nil
	}
	var session string
	for _, src := range sessionSources {
		if v, exists := headerValue(req.Headers, src); exists {
			if v == "" {
				return empty, nil
			}
			session = v
			break
		}
	}
	if session == "" {
		session = sessionFallback(req.Metadata)
	}
	if session == "" {
		return empty, nil
	}
	out := map[string][]string{targetSessionHeader: {session}}
	// Identify the proxy only when the client didn't identify itself.
	if !headerExists(req.Headers, targetClientHeader) {
		out[targetClientHeader] = []string{defaultClientValue}
	}
	return okEnvelopeJSON(interceptResponse{Headers: out})
}

// sessionFallback returns the host-computed conversation identity for clients
// that send no session header of their own (notably DeepSeek Harness, whose
// pi-ai transport gates session headers behind a switch its adapter withholds —
// so the wire carries nothing to map).
//
// canonical_session_id is built by the core from the source protocol and the
// client's own session id, yielding e.g. "claude:<uuid>" or "codex:<id>". It is
// populated only on the after-auth pass, which is why this is a fallback and
// not a source: a request that already carries a recognized header still wins
// on the before-auth pass, unchanged.
//
// Only consulted when every client-supplied source was absent, so a client
// that does identify itself is never overridden. derived_session_id and
// lcp_affinity_session_id are deliberately NOT used: the former is absent from
// this payload on v7.2.157, and core documents the latter as unusable for
// provider conversation identity.
func sessionFallback(metadata map[string]any) string {
	if metadata == nil {
		return ""
	}
	v, ok := metadata["canonical_session_id"]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	value, valid := normalizeSessionID(s)
	if !valid {
		return ""
	}
	return value
}

func okEnvelopeJSON(result any) ([]byte, error) {
	raw, errMarshal := json.Marshal(result)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return json.Marshal(envelope{OK: true, Result: json.RawMessage(raw)})
}

func errorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message}})
	return raw
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		return
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
}
