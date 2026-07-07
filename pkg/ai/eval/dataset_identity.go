package eval

import (
	"fmt"
	"strings"
)

// DatasetIdentity 是评估数据集的完整身份。
//
// name 和 version 必须作为一个整体出现：单独的 v1.2.0 无法说明它属于 agent golden、
// RAG retrieval golden 还是 provider failover smoke。把它建模为值对象，可以防止
// report、evidence、runner 各自散落字段后出现“只有 version 没有 name”的非法状态。
type DatasetIdentity struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

func normalizeDatasetIdentity(identity DatasetIdentity) DatasetIdentity {
	return DatasetIdentity{
		Name:    strings.TrimSpace(identity.Name),
		Version: strings.TrimSpace(identity.Version),
	}
}

func validateDatasetIdentity(identity DatasetIdentity) error {
	normalized := normalizeDatasetIdentity(identity)
	if normalized.Name == "" {
		return fmt.Errorf("dataset_identity dataset_name is required")
	}
	if normalized.Version == "" {
		return fmt.Errorf("dataset_identity dataset_version is required")
	}
	return nil
}
