// Package apperror 提供 pkg/ai 内部使用的错误分类辅助。
//
// 这个包不定义对外公开契约。对外契约仍然属于各业务包，例如 llm.ErrUpstream。
// apperror 的价值是让 adapter/resilience/eval 等内部模块可以共享一套错误分类，
// 同时保留原始 cause，方便 errors.Is / errors.As 和日志诊断。
package apperror

import (
	"errors"
	"fmt"
)

// Class 描述错误的工程语义。
//
// 这里刻意用少量分类覆盖 P0 需要的边界：调用方错误、上游错误、协议错误和内部错误。
// 后续 resilience 会根据这些分类决定是否重试、熔断或快速失败。
type Class string

const (
	ClassCaller   Class = "caller"
	ClassUpstream Class = "upstream"
	ClassProtocol Class = "protocol"
	ClassInternal Class = "internal"
)

var (
	// ErrCaller 表示调用方输入、认证、权限等不可重试错误。
	ErrCaller = errors.New("ai: caller error")
	// ErrUpstream 表示上游服务不可用、限流、超时等可重试/可降级错误。
	ErrUpstream = errors.New("ai: upstream error")
	// ErrProtocol 表示 adapter 无法理解上游响应，通常需要修 adapter 或处理供应商协议漂移。
	ErrProtocol = errors.New("ai: protocol error")
	// ErrInternal 表示框架内部未预期错误。
	ErrInternal = errors.New("ai: internal error")
)

// Error 是带分类和 cause 的内部错误。
//
// Message 面向开发者诊断，不应放入敏感原文；Cause 保留底层错误，用于 errors.Is/As。
type Error struct {
	Class   Class
	Message string
	Cause   error
}

// New 创建一个分类错误。
func New(class Class, message string, cause error) error {
	return &Error{
		Class:   class,
		Message: message,
		Cause:   cause,
	}
}

// Wrap 只在 cause 非空时创建分类错误，便于调用方保留 Go 里 “nil error 不包装” 的习惯。
func Wrap(class Class, message string, cause error) error {
	if cause == nil {
		return nil
	}
	return New(class, message, cause)
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Cause == nil {
		return fmt.Sprintf("%s: %s", e.Class, e.Message)
	}
	return fmt.Sprintf("%s: %s: %v", e.Class, e.Message, e.Cause)
}

// Unwrap 保留底层 cause。
//
// 例如 adapter 可以把 llm.ErrUpstream 作为 cause 传入，这样上层仍能
// errors.Is(err, llm.ErrUpstream)，不会被 apperror 的内部分类吞掉。
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// Is 让 errors.Is 能按内部分类 sentinel 判断。
func (e *Error) Is(target error) bool {
	if e == nil {
		return false
	}

	return target == sentinelForClass(e.Class)
}

func sentinelForClass(class Class) error {
	switch class {
	case ClassCaller:
		return ErrCaller
	case ClassUpstream:
		return ErrUpstream
	case ClassProtocol:
		return ErrProtocol
	case ClassInternal:
		return ErrInternal
	default:
		return nil
	}
}
