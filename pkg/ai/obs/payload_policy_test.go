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
				Mode:        PayloadModeMetadataOnly,
				Environment: "production",
			},
			wantMode: PayloadModeMetadataOnly,
		},
		{
			name: "redacted content is accepted in production",
			input: PayloadPolicyInput{
				Mode:        PayloadModeContentRedacted,
				Environment: "production",
			},
			wantMode: PayloadModeContentRedacted,
		},
		{
			name: "raw content is accepted only in local isolated environments",
			input: PayloadPolicyInput{
				Mode:        PayloadModeContentRaw,
				Environment: "local",
			},
			wantMode: PayloadModeContentRaw,
		},
		{
			name: "production rejects raw content even when debug is enabled",
			input: PayloadPolicyInput{
				Mode:        PayloadModeContentRaw,
				Environment: "production",
				Debug:       true,
			},
			wantErrField: "payload mode",
		},
		{
			name: "debug does not upgrade metadata only to raw content",
			input: PayloadPolicyInput{
				Mode:        PayloadModeMetadataOnly,
				Environment: "local",
				Debug:       true,
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
			if got.Mode != tt.wantMode {
				t.Fatalf("Mode = %q, want %q", got.Mode, tt.wantMode)
			}
		})
	}
}

func TestPayloadPolicySanitizeKeepsSensitiveValuesOutOfEveryMode(t *testing.T) {
	const (
		safeInput         = "summarize the supplied public document"
		safeOutput        = "summary complete"
		syntheticBearer   = "Bearer t012-synthetic-token"
		syntheticPII      = "t012.user@example.test"
		syntheticToolArgs = `{"account":"demo","password":"t012-password"}`
	)

	tests := []struct {
		name         string
		mode         PayloadMode
		wantInput    bool
		wantOutput   bool
		wantToolArgs bool
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
			name:       "raw content retains safe content but still strips secrets and PII",
			mode:       PayloadModeContentRaw,
			wantInput:  true,
			wantOutput: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policy, err := ResolvePayloadPolicy(PayloadPolicyInput{
				Mode:        tt.mode,
				Environment: "local",
			})
			if err != nil {
				t.Fatal("ResolvePayloadPolicy() returned an unexpected error")
			}

			// 强制扫描发生在所有模式、所有内容字段进入 trace/log/queue 之前；raw 只是
			// 允许受控普通内容，不是关闭 secret 或 PII 检测的旁路。
			snapshot := policy.Sanitize(PayloadContent{
				Input:         safeInput + " " + syntheticBearer,
				Output:        safeOutput + " " + syntheticPII,
				Authorization: syntheticBearer,
				UserReference: syntheticPII,
				ToolArguments: syntheticToolArgs,
			})

			assertPayloadContentPresence(t, snapshot, tt.wantInput, tt.wantOutput, tt.wantToolArgs)
			assertPayloadSnapshotDoesNotLeak(t, snapshot, []string{
				syntheticBearer,
				syntheticPII,
				syntheticToolArgs,
			})
		})
	}
}

func assertPayloadContentPresence(t *testing.T, snapshot PayloadSnapshot, wantInput, wantOutput, wantToolArgs bool) {
	t.Helper()

	if (snapshot.Input != "") != wantInput {
		t.Fatalf("snapshot input presence = %v, want %v", snapshot.Input != "", wantInput)
	}
	if (snapshot.Output != "") != wantOutput {
		t.Fatalf("snapshot output presence = %v, want %v", snapshot.Output != "", wantOutput)
	}
	if (snapshot.ToolArguments != "") != wantToolArgs {
		t.Fatalf("snapshot tool arguments presence = %v, want %v", snapshot.ToolArguments != "", wantToolArgs)
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
