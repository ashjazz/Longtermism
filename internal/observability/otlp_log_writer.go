package observability

import (
	"context"

	otellog "go.opentelemetry.io/otel/log"
)

// OTLPHTTPCompletionLogWriter 把已收紧的 completion fact 映射到 OTel Logs API。
// Logger.Emit 没有同步 exporter error；异步发送、flush 与 shutdown 由同一个 provider
// lifecycle 管理，避免把 Collector 故障改写成 HTTP 业务失败。
type OTLPHTTPCompletionLogWriter struct {
	logger otellog.Logger
}

type HTTPCompletionLogFanoutWriter struct {
	writers []HTTPCompletionLogWriter
}

func NewHTTPCompletionLogFanoutWriter(writers ...HTTPCompletionLogWriter) HTTPCompletionLogWriter {
	filtered := make([]HTTPCompletionLogWriter, 0, len(writers))
	for _, writer := range writers {
		if writer != nil {
			filtered = append(filtered, writer)
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	return &HTTPCompletionLogFanoutWriter{writers: filtered}
}

func (w *HTTPCompletionLogFanoutWriter) Write(ctx context.Context, entry HTTPCompletionLog) error {
	var firstErr error
	for _, writer := range w.writers {
		if err := writer.Write(ctx, entry); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func NewOTLPHTTPCompletionLogWriter(logger otellog.Logger) (*OTLPHTTPCompletionLogWriter, error) {
	if logger == nil {
		return nil, errInvalidHTTPCompletionOTLP
	}
	return &OTLPHTTPCompletionLogWriter{logger: logger}, nil
}

func (w *OTLPHTTPCompletionLogWriter) Write(ctx context.Context, entry HTTPCompletionLog) error {
	if w == nil || w.logger == nil || ctx == nil {
		return errInvalidHTTPCompletionOTLP
	}
	projection, err := BuildHTTPCompletionOTLPRecord(entry)
	if err != nil {
		return err
	}
	var record otellog.Record
	record.SetTimestamp(projection.Timestamp)
	record.SetObservedTimestamp(projection.Timestamp)
	record.SetSeverityText(projection.Severity)
	record.SetSeverity(completionSeverity(projection.Severity))
	record.SetBody(otellog.StringValue(projection.Body))
	attributes := make([]otellog.KeyValue, 0, len(projection.Attributes))
	for key, value := range projection.Attributes {
		switch typed := value.(type) {
		case string:
			attributes = append(attributes, otellog.String(key, typed))
		case int64:
			attributes = append(attributes, otellog.Int64(key, typed))
		default:
			return errInvalidHTTPCompletionOTLP
		}
	}
	record.AddAttributes(attributes...)
	w.logger.Emit(ctx, record)
	return nil
}

func completionSeverity(value string) otellog.Severity {
	if value == "ERROR" {
		return otellog.SeverityError
	}
	return otellog.SeverityInfo
}
