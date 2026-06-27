package rag

import (
	"context"
	"testing"
)

func TestRecursiveChunkerChunksDocuments(t *testing.T) {
	tests := []struct {
		name     string
		doc      Document
		config   RecursiveChunkerConfig
		want     []wantChunk
		wantMeta map[string]any
	}{
		{
			name: "empty document returns no chunks",
			doc: Document{
				ID:      "doc-empty",
				Content: "   \n\t  ",
				Source:  "empty.md",
				Type:    "markdown",
				Meta: map[string]any{
					"tenant_id": "tenant-a",
				},
			},
			config: RecursiveChunkerConfig{
				ChunkSize: 10,
				Overlap:   2,
			},
			want: nil,
		},
		{
			name: "short document returns one chunk with metadata copy",
			doc: Document{
				ID:      "doc-short",
				Content: "hello world",
				Source:  "short.md",
				Type:    "markdown",
				Meta: map[string]any{
					"tenant_id": "tenant-a",
					"page":      1,
				},
			},
			config: RecursiveChunkerConfig{
				ChunkSize: 20,
				Overlap:   4,
			},
			want: []wantChunk{
				{
					id:       "doc-short:chunk:0000",
					content:  "hello world",
					parentID: "doc-short",
				},
			},
			wantMeta: map[string]any{
				"tenant_id": "tenant-a",
				"page":      1,
				"source":    "short.md",
				"type":      "markdown",
			},
		},
		{
			name: "long document uses configured overlap",
			doc: Document{
				ID:      "doc-long",
				Content: "abcdefghijklmnopqrstuvwxyz",
				Source:  "letters.txt",
				Type:    "text",
				Meta: map[string]any{
					"tenant_id": "tenant-a",
				},
			},
			config: RecursiveChunkerConfig{
				ChunkSize: 10,
				Overlap:   3,
			},
			want: []wantChunk{
				{
					id:       "doc-long:chunk:0000",
					content:  "abcdefghij",
					parentID: "doc-long",
				},
				{
					id:       "doc-long:chunk:0001",
					content:  "hijklmnopq",
					parentID: "doc-long",
				},
				{
					id:       "doc-long:chunk:0002",
					content:  "opqrstuvwx",
					parentID: "doc-long",
				},
				{
					id:       "doc-long:chunk:0003",
					content:  "vwxyz",
					parentID: "doc-long",
				},
			},
			wantMeta: map[string]any{
				"tenant_id": "tenant-a",
				"source":    "letters.txt",
				"type":      "text",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunker := NewRecursiveChunker(tt.config)

			chunks, err := chunker.Chunk(context.Background(), tt.doc)

			if err != nil {
				t.Fatalf("Chunk() error = %v", err)
			}
			assertChunks(t, chunks, tt.want)
			if len(tt.wantMeta) > 0 {
				assertMetadata(t, chunks, tt.wantMeta)
			}
		})
	}
}

func TestRecursiveChunkerRejectsInvalidConfig(t *testing.T) {
	tests := []struct {
		name   string
		config RecursiveChunkerConfig
	}{
		{
			name: "chunk size is required",
			config: RecursiveChunkerConfig{
				ChunkSize: 0,
				Overlap:   0,
			},
		},
		{
			name: "overlap must be smaller than chunk size",
			config: RecursiveChunkerConfig{
				ChunkSize: 10,
				Overlap:   10,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunker := NewRecursiveChunker(tt.config)

			_, err := chunker.Chunk(context.Background(), Document{
				ID:      "doc-invalid",
				Content: "content",
			})

			if err == nil {
				t.Fatalf("Chunk() error = nil, want invalid config error")
			}
		})
	}
}

func TestRecursiveChunkerRespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	chunker := NewRecursiveChunker(RecursiveChunkerConfig{
		ChunkSize: 10,
		Overlap:   2,
	})

	_, err := chunker.Chunk(ctx, Document{
		ID:      "doc-cancelled",
		Content: "this content should not be chunked",
	})

	if err == nil {
		t.Fatalf("Chunk() error = nil, want context cancellation error")
	}
}

func TestRecursiveChunkerDoesNotMutateDocumentMetadata(t *testing.T) {
	doc := Document{
		ID:      "doc-meta",
		Content: "metadata must be copied",
		Source:  "meta.md",
		Type:    "markdown",
		Meta: map[string]any{
			"tenant_id": "tenant-a",
			"tags":      []string{"rag", "chunk"},
		},
	}
	chunker := NewRecursiveChunker(RecursiveChunkerConfig{
		ChunkSize: 50,
		Overlap:   5,
	})

	chunks, err := chunker.Chunk(context.Background(), doc)
	if err != nil {
		t.Fatalf("Chunk() error = %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("chunks length = %d, want 1", len(chunks))
	}

	chunks[0].Metadata["tenant_id"] = "mutated"
	chunks[0].Metadata["tags"].([]string)[0] = "mutated"

	if doc.Meta["tenant_id"] != "tenant-a" {
		t.Fatalf("document metadata tenant_id = %q, want tenant-a", doc.Meta["tenant_id"])
	}
	if doc.Meta["tags"].([]string)[0] != "rag" {
		t.Fatalf("document metadata tags = %#v, want original tags", doc.Meta["tags"])
	}
}

type wantChunk struct {
	id       string
	content  string
	parentID string
}

func assertChunks(t *testing.T, got []Chunk, want []wantChunk) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("chunks length = %d, want %d: %#v", len(got), len(want), got)
	}
	for index, wantChunk := range want {
		chunk := got[index]
		if chunk.ID != wantChunk.id {
			t.Fatalf("chunk[%d].ID = %q, want %q", index, chunk.ID, wantChunk.id)
		}
		if chunk.Content != wantChunk.content {
			t.Fatalf("chunk[%d].Content = %q, want %q", index, chunk.Content, wantChunk.content)
		}
		if chunk.ParentID != wantChunk.parentID {
			t.Fatalf("chunk[%d].ParentID = %q, want %q", index, chunk.ParentID, wantChunk.parentID)
		}
	}
}

func assertMetadata(t *testing.T, chunks []Chunk, want map[string]any) {
	t.Helper()

	for chunkIndex, chunk := range chunks {
		if chunk.Metadata == nil {
			t.Fatalf("chunk[%d].Metadata is nil", chunkIndex)
		}
		for key, wantValue := range want {
			gotValue, ok := chunk.Metadata[key]
			if !ok {
				t.Fatalf("chunk[%d].Metadata[%q] is missing", chunkIndex, key)
			}
			if gotValue != wantValue {
				t.Fatalf("chunk[%d].Metadata[%q] = %#v, want %#v", chunkIndex, key, gotValue, wantValue)
			}
		}
	}
}
