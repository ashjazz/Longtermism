package obs

import (
	"encoding/json"
	"fmt"
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
			name: "raw content is accepted only with local explicit opt in",
			input: PayloadPolicyInput{
				Mode:              PayloadModeContentRaw,
				Environment:       "local",
				RawContentEnabled: true,
			},
			wantMode: PayloadModeContentRaw,
		},
		{
			name: "raw content is accepted in test with explicit opt in",
			input: PayloadPolicyInput{
				Mode:              PayloadModeContentRaw,
				Environment:       "test",
				RawContentEnabled: true,
			},
			wantMode: PayloadModeContentRaw,
		},
		{
			name: "raw content without explicit opt in is rejected",
			input: PayloadPolicyInput{
				Mode:        PayloadModeContentRaw,
				Environment: "local",
			},
			wantErrField: "raw content",
		},
		{
			name: "raw content is rejected outside local and test",
			input: PayloadPolicyInput{
				Mode:              PayloadModeContentRaw,
				Environment:       "production",
				RawContentEnabled: true,
			},
			wantErrField: "environment",
		},
		{
			name: "raw content is rejected in an unknown environment",
			input: PayloadPolicyInput{
				Mode:              PayloadModeContentRaw,
				Environment:       "preview",
				RawContentEnabled: true,
			},
			wantErrField: "environment",
		},
		{
			name: "raw opt in is rejected for a redacted policy",
			input: PayloadPolicyInput{
				Mode:              PayloadModeContentRedacted,
				RawContentEnabled: true,
			},
			wantErrField: "raw content",
		},
		{
			name: "unknown mode is rejected without echoing its value",
			input: PayloadPolicyInput{
				Mode: PayloadMode("t024-unknown-mode-canary"),
			},
			wantErrField: "payload mode",
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
				if strings.Contains(err.Error(), string(tt.input.Mode)) {
					t.Fatal("payload policy error reflected an untrusted mode value")
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
		{
			name:       "raw mode keeps the export snapshot redacted",
			mode:       PayloadModeContentRaw,
			wantInput:  true,
			wantOutput: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := PayloadPolicyInput{Mode: tt.mode}
			if tt.mode == PayloadModeContentRaw {
				input.Environment = "local"
				input.RawContentEnabled = true
			}
			policy, err := ResolvePayloadPolicy(input)
			if err != nil {
				t.Fatal("ResolvePayloadPolicy() returned an unexpected error")
			}

			// 标准快照是唯一可进入 trace/log/queue 的类型；即使 raw 已启用，这条
			// 外发路径仍必须脱敏，完整原文只能通过单独的本地调试工件取得。
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

func TestPayloadPolicyLocalRawPayloadIsExactAndCannotBeSerialized(t *testing.T) {
	const (
		syntheticBearer = "Bearer t012-synthetic-token"
		syntheticPII    = "t012.user@example.test"
	)
	content := PayloadContent{
		Input:  "prompt " + syntheticBearer,
		Output: "answer " + syntheticPII,
	}

	rawPolicy, err := ResolvePayloadPolicy(PayloadPolicyInput{
		Mode:              PayloadModeContentRaw,
		Environment:       "local",
		RawContentEnabled: true,
	})
	if err != nil {
		t.Fatal("ResolvePayloadPolicy() returned an unexpected error")
	}

	localPayload, err := rawPolicy.LocalRawPayload(content)
	if err != nil {
		t.Fatal("LocalRawPayload() returned an unexpected error")
	}
	if localPayload.Input() != content.Input || localPayload.Output() != content.Output {
		t.Fatal("LocalRawPayload() did not retain the exact local content")
	}
	if _, err := json.Marshal(localPayload); err == nil {
		t.Fatal("LocalRawPayload() was JSON serializable")
	}
	for _, rendered := range []string{
		fmt.Sprintf("%v", localPayload),
		fmt.Sprintf("%+v", localPayload),
		fmt.Sprintf("%#v", localPayload),
	} {
		if strings.Contains(rendered, syntheticBearer) || strings.Contains(rendered, syntheticPII) {
			t.Fatal("LocalRawPayload() leaked complete content through fmt")
		}
	}

	// 对比标准导出快照，避免未来 exporter 将 raw 调试工件当成可外发 payload。
	exportSnapshot, err := json.Marshal(rawPolicy.Sanitize(content))
	if err != nil {
		t.Fatal("marshal raw policy export snapshot")
	}
	if strings.Contains(string(exportSnapshot), syntheticBearer) || strings.Contains(string(exportSnapshot), syntheticPII) {
		t.Fatal("raw local content leaked into the standard export snapshot")
	}
}

func TestPayloadPolicyLocalRawPayloadFailsClosedOutsideRawMode(t *testing.T) {
	for _, mode := range []PayloadMode{PayloadModeMetadataOnly, PayloadModeContentRedacted} {
		t.Run(string(mode), func(t *testing.T) {
			policy, err := ResolvePayloadPolicy(PayloadPolicyInput{Mode: mode})
			if err != nil {
				t.Fatal("ResolvePayloadPolicy() returned an unexpected error")
			}
			if _, err := policy.LocalRawPayload(PayloadContent{Input: "must not escape"}); err == nil {
				t.Fatal("LocalRawPayload() error = nil, want non-raw policy rejected")
			}
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
