package observability

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestJSONLHTTPCompletionLogWriterWritesOnlyCompletionSchema(t *testing.T) {
	tests := []struct{ name string }{{name: "writes one JSON object per line"}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			writer, err := NewJSONLHTTPCompletionLogWriter(&output)
			if err != nil {
				t.Fatalf("NewJSONLHTTPCompletionLogWriter() error = %v", err)
			}
			entry, err := BuildHTTPCompletionLog(newHTTPCompletionLogInput(false, false, false))
			if err != nil {
				t.Fatalf("BuildHTTPCompletionLog() error = %v", err)
			}
			if err := writer.Write(context.Background(), entry); err != nil {
				t.Fatalf("Write() error = %v", err)
			}
			for _, forbidden := range []string{"prompt", "output", "authorization", "api_key", "provider_error_body"} {
				if strings.Contains(strings.ToLower(output.String()), forbidden) {
					t.Fatalf("JSONL output leaked forbidden field %q: %s", forbidden, output.String())
				}
			}
			if !strings.HasSuffix(output.String(), "\n") || !strings.Contains(output.String(), `"request_id"`) {
				t.Fatalf("JSONL output = %q, want newline-delimited completion schema", output.String())
			}
		})
	}
}
