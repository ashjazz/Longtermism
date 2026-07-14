package obs

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestResolvePayloadPolicy(t *testing.T) {
	tests := []struct {
		name         string
		input        PayloadPolicyInput
		wantMode     PayloadMode
		wantErrField string
	}{
		{
			name: "metadata only is accepted in production",
			input: PayloadPolicyInput{
				Mode: PayloadModeMetadataOnly,
			},
			wantMode: PayloadModeMetadataOnly,
		},
		{
			name: "redacted content is accepted in production",
			input: PayloadPolicyInput{
				Mode: PayloadModeContentRedacted,
			},
			wantMode: PayloadModeContentRedacted,
		},
		{
			name: "removed raw mode is rejected even when debug is enabled",
			input: PayloadPolicyInput{
				Mode:  PayloadMode("content_raw"),
				Debug: true,
			},
			wantErrField: "payload mode",
		},
		{
			name: "debug does not upgrade metadata only to content capture",
			input: PayloadPolicyInput{
				Mode:  PayloadModeMetadataOnly,
				Debug: true,
			},
			wantMode: PayloadModeMetadataOnly,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolvePayloadPolicy(tt.input)
			if tt.wantErrField != "" {
				if err == nil {
					t.Fatalf("ResolvePayloadPolicy() error = nil, want field %q", tt.wantErrField)
				}
				if !strings.Contains(err.Error(), tt.wantErrField) {
					t.Fatalf("payload policy error did not contain expected field %q", tt.wantErrField)
				}
				return
			}
			if err != nil {
				t.Fatal("ResolvePayloadPolicy() returned an unexpected error")
			}
			if got.Mode() != tt.wantMode {
				t.Fatalf("Mode = %q, want %q", got.Mode(), tt.wantMode)
			}
		})
	}
}

func TestPayloadPolicySanitizeKeepsSensitiveValuesOutOfEveryMode(t *testing.T) {
	const (
		safeInput       = "summarize the supplied public document"
		safeOutput      = "summary complete"
		syntheticBearer = "Bearer t012-synthetic-token"
		syntheticPII    = "t012.user@example.test"
	)

	tests := []struct {
		name       string
		mode       PayloadMode
		wantInput  bool
		wantOutput bool
	}{
		{
			name: "metadata only omits all content",
			mode: PayloadModeMetadataOnly,
		},
		{
			name:       "redacted content retains only safe controlled content",
			mode:       PayloadModeContentRedacted,
			wantInput:  true,
			wantOutput: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policy, err := ResolvePayloadPolicy(PayloadPolicyInput{
				Mode: tt.mode,
			})
			if err != nil {
				t.Fatal("ResolvePayloadPolicy() returned an unexpected error")
			}

			// 强制扫描发生在所有模式、所有内容字段进入 trace/log/queue 之前；raw 只是
			// 允许受控普通内容，不是关闭 secret 或 PII 检测的旁路。
			snapshot := policy.Sanitize(PayloadContent{
				Input:  safeInput + " " + syntheticBearer,
				Output: safeOutput + " " + syntheticPII,
			})

			assertPayloadContentPresence(t, snapshot, tt.wantInput, tt.wantOutput)
			assertPayloadSnapshotDoesNotLeak(t, snapshot, []string{
				syntheticBearer,
				syntheticPII,
			})
		})
	}
}

func TestPayloadPolicySanitizeRemovesEmbeddedCredentialsAndPII(t *testing.T) {
	const (
		syntheticBasic  = "QmVhcmVyIHRoZS1yYXctdDAyNC10b2tlbg=="
		syntheticToken  = "t024-opaque-token"
		syntheticTokenA = "t024-token-equals"
		syntheticTokenB = "t024-token-colon"
		syntheticCookie = "t024-session-cookie"
		syntheticPhone  = "13800138000"
		syntheticID     = "110101199001011234"
	)

	for _, mode := range []PayloadMode{PayloadModeContentRedacted} {
		t.Run(string(mode), func(t *testing.T) {
			policy, err := ResolvePayloadPolicy(PayloadPolicyInput{Mode: mode})
			if err != nil {
				t.Fatal("ResolvePayloadPolicy() returned an unexpected error")
			}
			snapshot := policy.Sanitize(PayloadContent{
				Input:  "safe input Authorization: Basic " + syntheticBasic + " Cookie: " + syntheticCookie,
				Output: "safe output Token " + syntheticToken + " Token=" + syntheticTokenA + " Token:" + syntheticTokenB + " phone " + syntheticPhone + " id " + syntheticID,
			})
			assertPayloadSnapshotDoesNotLeak(t, snapshot, []string{syntheticBasic, syntheticToken, syntheticTokenA, syntheticTokenB, syntheticCookie, syntheticPhone, syntheticID})
		})
	}
}

func TestPayloadPolicySanitizeFailsClosedWhenPolicyWasNotResolved(t *testing.T) {
	snapshot := (PayloadPolicy{}).Sanitize(PayloadContent{
		Input:  "safe input",
		Output: "safe output",
	})
	if snapshot.Input() != "" || snapshot.Output() != "" {
		t.Fatal("an unresolved payload policy emitted content")
	}
}

func assertPayloadContentPresence(t *testing.T, snapshot PayloadSnapshot, wantInput, wantOutput bool) {
	t.Helper()

	if (snapshot.Input() != "") != wantInput {
		t.Fatalf("snapshot input presence = %v, want %v", snapshot.Input() != "", wantInput)
	}
	if (snapshot.Output() != "") != wantOutput {
		t.Fatalf("snapshot output presence = %v, want %v", snapshot.Output() != "", wantOutput)
	}
}

func assertPayloadSnapshotDoesNotLeak(t *testing.T, snapshot PayloadSnapshot, forbiddenValues []string) {
	t.Helper()

	payload, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal("marshal payload snapshot")
	}
	for _, forbidden := range forbiddenValues {
		if strings.Contains(string(payload), forbidden) {
			t.Fatal("payload snapshot leaked a synthetic sensitive value")
		}
	}
}
