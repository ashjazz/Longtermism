package rag

import (
	"context"
	"fmt"
	"strings"
)

// RecursiveChunkerConfig 描述递归切分器的核心参数。
//
// ChunkSize 和 Overlap 使用 rune 数量而非 byte 数量，避免中文、emoji 等多字节字符
// 被切坏。P2 初始版本先落地确定性的字符窗口；后续可以在同一接口下增加 Markdown、
// 段落、句子、代码分隔符等递归优先级。
type RecursiveChunkerConfig struct {
	ChunkSize int
	Overlap   int
}

// RecursiveChunker 是 P2 RAG 主线的默认切分器。
//
// 它的目标不是“一步做到最聪明的语义切分”，而是先给文档 -> chunk -> eval 建立稳定、
// 可复现的输入边界。稳定 chunk ID 和 metadata 复制对后续向量库 upsert、删除和回归评估
// 都很关键。
type RecursiveChunker struct {
	config RecursiveChunkerConfig
}

// NewRecursiveChunker 创建递归切分器。
//
// 构造函数不提前 panic 或返回 error，是为了让它可以作为配置对象被装配；真正使用时
// 在 Chunk 中 fail fast，并带上可诊断错误。
func NewRecursiveChunker(config RecursiveChunkerConfig) *RecursiveChunker {
	return &RecursiveChunker{config: config}
}

// Chunk 将源文档切分为稳定、有父文档关联的 chunks。
//
// 生产 RAG 的一个常见故障是“chunk 边界变化导致召回指标忽然漂移”。因此这里的初始版本
// 选择完全确定性的滑动窗口：同一 doc.ID、content、chunk size 和 overlap 会得到同一组 ID。
func (c *RecursiveChunker) Chunk(ctx context.Context, doc Document) ([]Chunk, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if err := validateRecursiveConfig(c.config); err != nil {
		return nil, err
	}
	if strings.TrimSpace(doc.Content) == "" {
		return nil, nil
	}

	contentRunes := []rune(doc.Content)
	step := c.config.ChunkSize - c.config.Overlap
	chunks := make([]Chunk, 0, estimateChunkCount(len(contentRunes), c.config.ChunkSize, step))
	for start, index := 0, 0; start < len(contentRunes); start, index = start+step, index+1 {
		if err := contextErr(ctx); err != nil {
			return nil, err
		}

		end := start + c.config.ChunkSize
		if end > len(contentRunes) {
			end = len(contentRunes)
		}
		chunks = append(chunks, Chunk{
			ID:       stableChunkID(doc.ID, index),
			Content:  string(contentRunes[start:end]),
			ParentID: doc.ID,
			Metadata: chunkMetadata(doc),
		})
	}
	return chunks, nil
}

func validateRecursiveConfig(config RecursiveChunkerConfig) error {
	if config.ChunkSize <= 0 {
		return fmt.Errorf("recursive chunker chunk size must be positive")
	}
	if config.Overlap < 0 {
		return fmt.Errorf("recursive chunker overlap must not be negative")
	}
	if config.Overlap >= config.ChunkSize {
		return fmt.Errorf("recursive chunker overlap must be smaller than chunk size")
	}
	return nil
}

func estimateChunkCount(contentLen, chunkSize, step int) int {
	if contentLen <= 0 {
		return 0
	}
	if contentLen <= chunkSize {
		return 1
	}
	return ((contentLen-chunkSize)+step-1)/step + 1
}

func stableChunkID(documentID string, index int) string {
	return fmt.Sprintf("%s:chunk:%04d", documentID, index)
}

func chunkMetadata(doc Document) map[string]any {
	metadata := cloneMetadata(doc.Meta)
	if doc.Source != "" {
		metadata["source"] = doc.Source
	}
	if doc.Type != "" {
		metadata["type"] = doc.Type
	}
	return metadata
}

func cloneMetadata(source map[string]any) map[string]any {
	cloned := make(map[string]any, len(source)+2)
	for key, value := range source {
		cloned[key] = cloneMetadataValue(value)
	}
	return cloned
}

func cloneMetadataValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneMetadata(typed)
	case []any:
		cloned := make([]any, len(typed))
		for index, item := range typed {
			cloned[index] = cloneMetadataValue(item)
		}
		return cloned
	case []string:
		return append([]string(nil), typed...)
	default:
		return value
	}
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
