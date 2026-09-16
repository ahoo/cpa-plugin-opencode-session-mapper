// SPDX-License-Identifier: MIT
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	pluginID          = "opencode-session-mapper"
	pluginRepository  = "https://github.com/ahoo/cpa-plugin-opencode-session-mapper"
	requestBodyBytes  = 6_378_698
	maxReadBytes      = 8 << 20
	syntheticMarker   = "mapper-large-envelope-marker-q7z"
	syntheticSession  = "123e4567-e89b-12d3-a456-426614174000"
	expectedClient    = "cliproxy"
	expectedModel     = "mock-model"
	managementPath    = "/v0/management/plugins"
	mockReportPath    = "/__mapper_report"
	expectedHostBuild = "2026-09-15T14:07:07Z"
)

type mockReport struct {
	Requests           int    `json:"requests"`
	SessionHeaderExact bool   `json:"session_header_exact"`
	ClientHeaderExact  bool   `json:"client_header_exact"`
	PathExact          bool   `json:"path_exact"`
	BodyValid          bool   `json:"body_valid"`
	BodyBytes          int    `json:"body_bytes"`
	BodySHA256         string `json:"body_sha256"`
}

type synchronizedMock struct {
	mu     sync.Mutex
	report mockReport
}

type pluginListResponse struct {
	PluginsEnabled bool              `json:"plugins_enabled"`
	Plugins        []pluginListEntry `json:"plugins"`
}

type pluginListEntry struct {
	ID               string          `json:"id"`
	Configured       bool            `json:"configured"`
	Registered       bool            `json:"registered"`
	Enabled          bool            `json:"enabled"`
	EffectiveEnabled bool            `json:"effective_enabled"`
	Metadata         *pluginMetadata `json:"metadata"`
}

type pluginMetadata struct {
	Name             string `json:"name"`
	Version          string `json:"version"`
	Author           string `json:"author"`
	GitHubRepository string `json:"github_repository"`
	Logo             string `json:"logo"`
	ConfigFields     []any  `json:"config_fields"`
}

type hostIdentity struct {
	Version         string `json:"version"`
	Commit          string `json:"commit"`
	BuildDate       string `json:"build_date"`
	ImageRepository string `json:"image_repository"`
	ImageDigest     string `json:"image_digest"`
}

type reportPlugin struct {
	ID            string `json:"id"`
	Version       string `json:"version"`
	LibrarySHA256 string `json:"library_sha256"`
}

type phaseAssertions struct {
	LargeEnvelopeForwarded bool `json:"large_envelope_forwarded"`
	SessionHeaderMapped    bool `json:"session_header_mapped"`
	ClientHeaderMapped     bool `json:"client_header_mapped"`
	MockRequestCountOne    bool `json:"mock_request_count_one"`
}

type phaseReport struct {
	SchemaVersion  int             `json:"schema_version"`
	SourceRevision string          `json:"source_revision"`
	OfficialHost   hostIdentity    `json:"official_host"`
	Plugin         reportPlugin    `json:"plugin"`
	Assertions     phaseAssertions `json:"assertions"`
}

type verifyOptions struct {
	hostURL        string
	mockURL        string
	managementKey  string
	apiKey         string
	pluginVersion  string
	pluginSHA256   string
	sourceRevision string
	imageRepo      string
	imageDigest    string
	hostVersion    string
	hostCommit     string
	hostBuildDate  string
	output         string
}

func main() {
	mode := flag.String("mode", "", "mock or verify")
	listen := flag.String("listen", ":9000", "mock listen address")
	hostURL := flag.String("host-url", "", "official Host base URL")
	mockURL := flag.String("mock-url", "", "mock upstream base URL")
	managementKey := flag.String("management-key", "", "synthetic management key")
	apiKey := flag.String("api-key", "", "synthetic client key")
	pluginVersion := flag.String("plugin-version", "", "candidate plugin version")
	pluginSHA256 := flag.String("plugin-sha256", "", "candidate library digest")
	sourceRevision := flag.String("source-revision", "", "candidate source revision")
	imageRepo := flag.String("image-repository", "", "official image repository")
	imageDigest := flag.String("image-digest", "", "official image digest")
	hostVersion := flag.String("host-version", "", "official Host version")
	hostCommit := flag.String("host-commit", "", "official Host commit")
	hostBuildDate := flag.String("host-build-date", "", "official Host build date")
	output := flag.String("output", "", "exclusive phase report path")
	flag.Parse()

	switch *mode {
	case "mock":
		if err := runMock(*listen); err != nil {
			fatal("mock failed")
		}
	case "verify":
		opts := verifyOptions{
			hostURL:        strings.TrimRight(*hostURL, "/"),
			mockURL:        strings.TrimRight(*mockURL, "/"),
			managementKey:  *managementKey,
			apiKey:         *apiKey,
			pluginVersion:  *pluginVersion,
			pluginSHA256:   *pluginSHA256,
			sourceRevision: *sourceRevision,
			imageRepo:      *imageRepo,
			imageDigest:    *imageDigest,
			hostVersion:    *hostVersion,
			hostCommit:     *hostCommit,
			hostBuildDate:  *hostBuildDate,
			output:         *output,
		}
		if err := runVerify(opts); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(1)
		}
	default:
		fatal("unknown harness mode")
	}
}

func runMock(listen string) error {
	state := &synchronizedMock{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+mockReportPath, func(w http.ResponseWriter, _ *http.Request) {
		state.mu.Lock()
		report := state.report
		state.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(report)
	})
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, maxReadBytes+1))
		if err != nil || len(body) > maxReadBytes {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		digest := sha256.Sum256(body)
		state.mu.Lock()
		state.report.Requests++
		state.report.SessionHeaderExact = len(r.Header.Values("X-Opencode-Session")) == 1 && r.Header.Get("X-Opencode-Session") == syntheticSession
		state.report.ClientHeaderExact = len(r.Header.Values("X-Opencode-Client")) == 1 && r.Header.Get("X-Opencode-Client") == expectedClient
		state.report.PathExact = r.URL.Path == "/v1/chat/completions"
		state.report.BodyValid = json.Valid(body)
		state.report.BodyBytes = len(body)
		state.report.BodySHA256 = hex.EncodeToString(digest[:])
		state.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-harness","object":"chat.completion","created":0,"model":"mock-model","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	})
	server := &http.Server{
		Addr:              listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	return server.ListenAndServe()
}

func runVerify(opts verifyOptions) error {
	if opts.hostURL == "" || opts.mockURL == "" || opts.managementKey == "" || opts.apiKey == "" ||
		opts.pluginVersion == "" || !isSHA256(opts.pluginSHA256) || !isRevision(opts.sourceRevision) ||
		opts.imageRepo == "" || !isImageDigest(opts.imageDigest) ||
		opts.hostVersion == "" || !isRevision(opts.hostCommit) ||
		opts.hostBuildDate != expectedHostBuild || opts.output == "" {
		return errors.New("invalid verifier options")
	}
	client := &http.Client{Timeout: 120 * time.Second}
	plugins, identity, err := waitForPlugin(client, opts)
	if err != nil {
		return err
	}
	if err = verifyPluginState(plugins, opts.pluginVersion); err != nil {
		return err
	}
	body, err := makeLargeRequestBody()
	if err != nil {
		return err
	}
	digest := sha256.Sum256(body)
	request, err := http.NewRequest(http.MethodPost, opts.hostURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return errors.New("could not construct model request")
	}
	request.Header.Set("Authorization", "Bearer "+opts.apiKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Claude-Code-Session-Id", syntheticSession)
	response, err := client.Do(request)
	if err != nil {
		return errors.New("model request failed")
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("model request returned status %d", response.StatusCode)
	}

	mock, err := fetchMockReport(client, opts.mockURL)
	if err != nil {
		return err
	}
	expectedDigest := hex.EncodeToString(digest[:])
	assertions := phaseAssertions{
		LargeEnvelopeForwarded: mock.BodyBytes == requestBodyBytes && mock.BodySHA256 == expectedDigest && mock.BodyValid && mock.PathExact,
		SessionHeaderMapped:    mock.SessionHeaderExact,
		ClientHeaderMapped:     mock.ClientHeaderExact,
		MockRequestCountOne:    mock.Requests == 1,
	}
	if !assertions.LargeEnvelopeForwarded || !assertions.SessionHeaderMapped ||
		!assertions.ClientHeaderMapped || !assertions.MockRequestCountOne {
		return errors.New("mock boundary assertions failed")
	}
	report := phaseReport{
		SchemaVersion:  1,
		SourceRevision: opts.sourceRevision,
		OfficialHost:   identity,
		Plugin: reportPlugin{
			ID:            pluginID,
			Version:       opts.pluginVersion,
			LibrarySHA256: opts.pluginSHA256,
		},
		Assertions: assertions,
	}
	return writeJSONExclusive(opts.output, report)
}

func waitForPlugin(client *http.Client, opts verifyOptions) (pluginListResponse, hostIdentity, error) {
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		request, err := http.NewRequest(http.MethodGet, opts.hostURL+managementPath, nil)
		if err != nil {
			return pluginListResponse{}, hostIdentity{}, errors.New("could not construct management request")
		}
		request.Header.Set("X-Management-Key", opts.managementKey)
		response, err := client.Do(request)
		if err == nil {
			plugins, identity, decodeErr := decodePluginList(response, opts)
			if decodeErr == nil && pluginReady(plugins) {
				return plugins, identity, nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return pluginListResponse{}, hostIdentity{}, errors.New("official Host did not become ready")
}

func decodePluginList(response *http.Response, opts verifyOptions) (pluginListResponse, hostIdentity, error) {
	defer response.Body.Close()
	if !isRevision(opts.hostCommit) {
		return pluginListResponse{}, hostIdentity{}, errors.New("official Host identity is invalid")
	}
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return pluginListResponse{}, hostIdentity{}, errors.New("management endpoint is not ready")
	}
	var plugins pluginListResponse
	if json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&plugins) != nil {
		return pluginListResponse{}, hostIdentity{}, errors.New("management response is invalid")
	}
	if response.Header.Get("X-CPA-VERSION") != opts.hostVersion ||
		response.Header.Get("X-CPA-COMMIT") != opts.hostCommit[:7] ||
		response.Header.Get("X-CPA-BUILD-DATE") != opts.hostBuildDate {
		return pluginListResponse{}, hostIdentity{}, errors.New("official Host identity mismatch")
	}
	return plugins, hostIdentity{
		Version:         opts.hostVersion,
		Commit:          opts.hostCommit,
		BuildDate:       opts.hostBuildDate,
		ImageRepository: opts.imageRepo,
		ImageDigest:     opts.imageDigest,
	}, nil
}

func pluginReady(response pluginListResponse) bool {
	if !response.PluginsEnabled || len(response.Plugins) != 1 {
		return false
	}
	entry := response.Plugins[0]
	return entry.ID == pluginID && entry.Configured && entry.Registered && entry.Enabled && entry.EffectiveEnabled
}

func verifyPluginState(response pluginListResponse, version string) error {
	if !pluginReady(response) {
		return errors.New("plugin is not effective")
	}
	metadata := response.Plugins[0].Metadata
	if metadata == nil || metadata.Name != pluginID || metadata.Version != version ||
		metadata.Author != "ahoo" || metadata.GitHubRepository != pluginRepository ||
		metadata.Logo != "" || len(metadata.ConfigFields) != 0 {
		return errors.New("plugin metadata mismatch")
	}
	return nil
}

func makeLargeRequestBody() ([]byte, error) {
	prefix := []byte(`{"model":"mock-model","messages":[{"role":"user","content":"` + syntheticMarker)
	suffix := []byte(`"}],"stream":false}`)
	padding := requestBodyBytes - len(prefix) - len(suffix)
	if padding <= 0 {
		return nil, errors.New("request fixture is too small")
	}
	body := make([]byte, 0, requestBodyBytes)
	body = append(body, prefix...)
	body = append(body, bytes.Repeat([]byte("a"), padding)...)
	body = append(body, suffix...)
	if len(body) != requestBodyBytes || !json.Valid(body) {
		return nil, errors.New("request fixture construction failed")
	}
	return body, nil
}

func fetchMockReport(client *http.Client, baseURL string) (mockReport, error) {
	response, err := client.Get(baseURL + mockReportPath)
	if err != nil {
		return mockReport{}, errors.New("mock report request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return mockReport{}, errors.New("mock report endpoint failed")
	}
	var report mockReport
	if json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&report) != nil {
		return mockReport{}, errors.New("mock report is invalid")
	}
	return report, nil
}

func isSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func isImageDigest(value string) bool {
	return strings.HasPrefix(value, "sha256:") && isSHA256(strings.TrimPrefix(value, "sha256:"))
}

func isRevision(value string) bool {
	return len(value) == 40 && isHex(value)
}

func isHex(value string) bool {
	for _, char := range value {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return true
}

func writeJSONExclusive(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return errors.New("could not encode report")
	}
	data = append(data, '\n')
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return errors.New("could not create report exclusively")
	}
	if _, err = file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return errors.New("could not write report")
	}
	if err = file.Close(); err != nil {
		_ = os.Remove(path)
		return errors.New("could not close report")
	}
	return nil
}

func fatal(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}
