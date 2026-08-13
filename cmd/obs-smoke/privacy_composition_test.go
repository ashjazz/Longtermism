package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDefaultPrivacyCommandRunnerPreflightsTheWholeConcreteGraph ensures optional platform
// configuration cannot fail halfway through a live run. Every credential, endpoint, contained
// store and collector binding is validated before the protected chat trigger can send anything.
func TestDefaultPrivacyCommandRunnerPreflightsTheWholeConcreteGraph(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer server.Close()
	valid := func() privacyCommandConfig {
		root := t.TempDir()
		return privacyCommandConfig{
			Profile: "grafana", Deadline: time.Now().UTC().Add(time.Minute), SurfaceTimeout: 250 * time.Millisecond,
			MasterSmokeEnabled: true, ChatSmokeEnabled: true, ApplicationURL: server.URL,
			ChatSmokeAuthorization: "t192-independent-chat-smoke-credential",
			ChatManifestRoot:       filepath.Join(root, "chat-manifests"), PrivacyArtifactRoot: filepath.Join(root, "privacy-artifacts"),
			TempoURL: server.URL, LokiURL: server.URL, LangfuseURL: server.URL,
			LangfuseCredential:           "t192-independent-langfuse-read-credential",
			ScoreProjectionPath:          filepath.Join(root, "score-projections.json"),
			CollectorRuntimeConfigDigest: "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
			CollectorComponentIdentity:   "otlphttp/loki", ExportAdmissionCorrelation: "admission-t192",
		}
	}

	configuration := valid()
	runner, err := newDefaultPrivacyCommandRunner(configuration)
	if err != nil || runner == nil {
		t.Fatalf("valid privacy graph was rejected with class %q", t192CommandClass(err))
	}
	if err := runner.Close(); err != nil {
		t.Fatal("privacy runner did not release its contained stores")
	}
	if calls != 0 {
		t.Fatal("privacy graph construction triggered network traffic")
	}

	tests := []func(*privacyCommandConfig){
		func(config *privacyCommandConfig) { config.MasterSmokeEnabled = false },
		func(config *privacyCommandConfig) { config.ChatSmokeEnabled = false },
		func(config *privacyCommandConfig) { config.ApplicationURL = "" },
		func(config *privacyCommandConfig) { config.ChatSmokeAuthorization = "" },
		func(config *privacyCommandConfig) { config.ChatManifestRoot = "" },
		func(config *privacyCommandConfig) { config.PrivacyArtifactRoot = "" },
		func(config *privacyCommandConfig) { config.TempoURL = "" },
		func(config *privacyCommandConfig) { config.LokiURL = "" },
		func(config *privacyCommandConfig) { config.LangfuseURL = "" },
		func(config *privacyCommandConfig) { config.LangfuseCredential = "" },
		func(config *privacyCommandConfig) { config.ScoreProjectionPath = "" },
		func(config *privacyCommandConfig) { config.CollectorRuntimeConfigDigest = "" },
		func(config *privacyCommandConfig) { config.CollectorComponentIdentity = "" },
		func(config *privacyCommandConfig) { config.ExportAdmissionCorrelation = "" },
		func(config *privacyCommandConfig) { config.SurfaceTimeout = 0 },
	}
	for index, mutate := range tests {
		configuration := valid()
		mutate(&configuration)
		if runner, err := newDefaultPrivacyCommandRunner(configuration); err == nil || runner != nil {
			t.Fatalf("incomplete privacy graph %d was accepted", index)
		} else {
			for _, forbidden := range []string{server.URL, configuration.ChatSmokeAuthorization, configuration.LangfuseCredential, configuration.ChatManifestRoot, configuration.ScoreProjectionPath, configuration.PrivacyArtifactRoot} {
				if forbidden != "" && strings.Contains(err.Error(), forbidden) {
					t.Fatal("privacy graph error exposed endpoint, credential, or contained path")
				}
			}
		}
		if calls != 0 {
			t.Fatal("privacy preflight failure triggered network traffic")
		}
		for _, path := range []string{configuration.ChatManifestRoot, configuration.PrivacyArtifactRoot, configuration.ScoreProjectionPath} {
			if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatal("privacy preflight failure created a contained store")
			}
		}
	}
}

func t192CommandClass(err error) string {
	type classified interface{ Class() string }
	if value, ok := err.(classified); ok {
		return value.Class()
	}
	if err == nil {
		return ""
	}
	return err.Error()
}
