package cmd

import (
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ashjazz/Longtermism/pkg/ai/obs"
	"github.com/gogf/gf/v2/errors/gerror"
)

// ObservabilityRuntimeMode 是应用观测运行时的唯一出口模式。
//
// noop 与 local 都不创建网络 exporter；只有 collector 模式可以把应用信号交给
// Collector。后端地址不属于此值对象，防止应用绕过单一 Collector 失败域。
type ObservabilityRuntimeMode string

const (
	ObservabilityRuntimeModeNoop      ObservabilityRuntimeMode = "noop"
	ObservabilityRuntimeModeLocal     ObservabilityRuntimeMode = "local"
	ObservabilityRuntimeModeCollector ObservabilityRuntimeMode = "collector"

	collectorProtocolGRPC         = "grpc"
	collectorProtocolHTTPProtobuf = "http_protobuf"
	productionEnvironment         = "production"
)

const maxCollectorTimeout = time.Minute

const (
	minimumChatSmokeCredentialBytes = 16
	maximumChatSmokeCredentialBytes = 512
	maximumChatSmokeReplayCapacity  = 4096
	maximumChatSmokeReplayTTL       = 10 * time.Minute
)

type ChatSmokeRuntimeConfigInput struct {
	Enabled              bool
	AuthorizationEnvName string
	AuthorizationValue   string
	ReplayCapacity       int
	ReplayTTL            time.Duration
}

// ChatSmokeRuntimeConfig is safe to print. The authorization value is consumed by the
// admission constructor and intentionally never becomes part of this snapshot.
type ChatSmokeRuntimeConfig struct {
	Ready                bool
	AuthorizationEnvName string
	CredentialPresent    bool
	ReplayCapacity       int
	ReplayTTL            time.Duration
}

func ResolveChatSmokeRuntimeConfig(input ChatSmokeRuntimeConfigInput) (ChatSmokeRuntimeConfig, error) {
	if !input.Enabled {
		return ChatSmokeRuntimeConfig{}, nil
	}
	authorization := strings.TrimSpace(input.AuthorizationValue)
	if !isValidEnvironmentVariableName(strings.TrimSpace(input.AuthorizationEnvName)) ||
		len(authorization) < minimumChatSmokeCredentialBytes || len(authorization) > maximumChatSmokeCredentialBytes ||
		strings.ContainsAny(authorization, "\r\n\x00") ||
		input.ReplayCapacity <= 0 || input.ReplayCapacity > maximumChatSmokeReplayCapacity ||
		input.ReplayTTL <= 0 || input.ReplayTTL > maximumChatSmokeReplayTTL {
		return ChatSmokeRuntimeConfig{}, gerror.New("chat smoke admission configuration is invalid")
	}
	return ChatSmokeRuntimeConfig{
		Ready: true, AuthorizationEnvName: strings.TrimSpace(input.AuthorizationEnvName),
		CredentialPresent: true, ReplayCapacity: input.ReplayCapacity, ReplayTTL: input.ReplayTTL,
	}, nil
}

// ObservabilityCollectorConfigInput 是配置加载层传入的临时 DTO。
// HeaderValue 仅用于判定凭据是否被提供，绝不能被保存到运行时快照、日志或错误中。
type ObservabilityCollectorConfigInput struct {
	Endpoint string
	Protocol string
	Timeout  string
	Insecure bool
	// AllowInsecure 仅允许在生产环境显式授权明文传输；默认 fail-fast。
	AllowInsecure bool
	// HeaderEnvName 是保存 OTLP 认证头的环境变量名，例如
	// OTEL_EXPORTER_OTLP_HEADERS。它可以进入低敏配置快照，方便诊断配置来源。
	HeaderEnvName string
	// HeaderValue 是配置加载边界读取到的实际认证头值。它只用于计算
	// CredentialPresent，绝不保存到运行时快照、日志或错误中。
	HeaderValue string
}

// ObservabilityRuntimeConfigInput 是运行配置解析的窄输入边界。
type ObservabilityRuntimeConfigInput struct {
	Mode        ObservabilityRuntimeMode
	Environment string
	Collector   ObservabilityCollectorConfigInput
	Payload     obs.PayloadPolicyInput
}

// ObservabilityCollectorConfig 是可安全打印、传递给 exporter 装配层的快照。
// 它由值类型组成，因此调用者不能通过共享可变容器反向修改已解析的配置。
type ObservabilityCollectorConfig struct {
	Endpoint          string
	Protocol          string
	Timeout           time.Duration
	Insecure          bool
	HeaderEnvName     string
	CredentialPresent bool
}

// ObservabilityRuntimeConfig 是经过 fail-fast 校验后的应用观测配置。
type ObservabilityRuntimeConfig struct {
	Mode             ObservabilityRuntimeMode
	Environment      string
	CollectorEnabled bool
	Collector        ObservabilityCollectorConfig
	Payload          obs.PayloadPolicy
}

// ResolveObservabilityRuntimeConfig 将原始应用配置解析为不可携带 secret 的运行快照。
// 显式选择 collector 却缺少必要参数时必须启动失败，而不是静默降级并丢失观测事实。
func ResolveObservabilityRuntimeConfig(input ObservabilityRuntimeConfigInput) (ObservabilityRuntimeConfig, error) {
	mode := input.Mode
	if mode == "" {
		return ObservabilityRuntimeConfig{}, gerror.New("observability mode is required")
	}
	environment := valueOrDefault(input.Environment, defaultObservabilityEnvironment)
	payload, err := resolvePayloadPolicy(environment, input.Payload)
	if err != nil {
		return ObservabilityRuntimeConfig{}, err
	}

	switch mode {
	case ObservabilityRuntimeModeNoop, ObservabilityRuntimeModeLocal:
		return ObservabilityRuntimeConfig{Mode: mode, Environment: environment, Payload: payload}, nil
	case ObservabilityRuntimeModeCollector:
		collector, err := resolveObservabilityCollectorConfig(environment, input.Collector)
		if err != nil {
			return ObservabilityRuntimeConfig{}, err
		}
		return ObservabilityRuntimeConfig{
			Mode:             mode,
			Environment:      environment,
			CollectorEnabled: true,
			Collector:        collector,
			Payload:          payload,
		}, nil
	default:
		return ObservabilityRuntimeConfig{}, gerror.New("observability mode is unsupported")
	}
}

// resolvePayloadPolicy 以应用 runtime environment 覆盖嵌套输入，防止调用方把
// production runtime 伪装成 local 来绕过 raw 内容门禁。未配置时默认 metadata-only，
// 这是安全降级而非对业务事实的猜测。
func resolvePayloadPolicy(environment string, input obs.PayloadPolicyInput) (obs.PayloadPolicy, error) {
	if input.Mode == "" {
		input.Mode = obs.PayloadModeMetadataOnly
	}
	input.Environment = environment
	return obs.ResolvePayloadPolicy(input)
}

func resolveObservabilityCollectorConfig(environment string, input ObservabilityCollectorConfigInput) (ObservabilityCollectorConfig, error) {
	endpoint := strings.TrimSpace(input.Endpoint)
	if endpoint == "" {
		return ObservabilityCollectorConfig{}, gerror.New("collector endpoint is required")
	}

	protocol := strings.TrimSpace(input.Protocol)
	if protocol == "" {
		protocol = collectorProtocolGRPC
	}
	if protocol != collectorProtocolGRPC && protocol != collectorProtocolHTTPProtobuf {
		return ObservabilityCollectorConfig{}, gerror.New("collector protocol is unsupported")
	}
	if !isValidCollectorEndpoint(protocol, endpoint) {
		return ObservabilityCollectorConfig{}, gerror.New("collector endpoint is invalid")
	}
	if collectorEndpointConflictsWithInsecureTransport(protocol, endpoint, input.Insecure) {
		return ObservabilityCollectorConfig{}, gerror.New("insecure collector transport conflicts with HTTPS endpoint")
	}

	timeout, err := time.ParseDuration(strings.TrimSpace(input.Timeout))
	if err != nil || timeout <= 0 || timeout > maxCollectorTimeout {
		return ObservabilityCollectorConfig{}, gerror.New("collector timeout must be positive")
	}

	insecure := input.Insecure || collectorEndpointUsesInsecureTransport(protocol, endpoint)
	if strings.EqualFold(strings.TrimSpace(environment), productionEnvironment) && insecure && !input.AllowInsecure {
		return ObservabilityCollectorConfig{}, gerror.New("insecure collector transport is not authorized in production")
	}
	headerEnvName := strings.TrimSpace(input.HeaderEnvName)
	if headerEnvName != "" && !isValidEnvironmentVariableName(headerEnvName) {
		return ObservabilityCollectorConfig{}, gerror.New("collector header environment name is invalid")
	}

	return ObservabilityCollectorConfig{
		Endpoint:          endpoint,
		Protocol:          protocol,
		Timeout:           timeout,
		Insecure:          insecure,
		HeaderEnvName:     headerEnvName,
		CredentialPresent: strings.TrimSpace(input.HeaderValue) != "",
	}, nil
}

// collectorEndpointUsesInsecureTransport 将 HTTP URL 的真实明文语义纳入 fail-fast
// 判断。否则 `http://...` + Insecure=false 会绕过生产授权，却仍被 HTTP exporter
// 以明文发送；gRPC 的 authority 形式仍只由显式 Insecure 控制。
func collectorEndpointUsesInsecureTransport(protocol, endpoint string) bool {
	if protocol != collectorProtocolHTTPProtobuf {
		return false
	}
	parsed, err := url.Parse(endpoint)
	return err == nil && parsed.Scheme == "http"
}

func collectorEndpointConflictsWithInsecureTransport(protocol, endpoint string, insecure bool) bool {
	if protocol != collectorProtocolHTTPProtobuf || !insecure {
		return false
	}
	parsed, err := url.Parse(endpoint)
	return err == nil && parsed.Scheme == "https"
}

func isValidCollectorEndpoint(protocol, endpoint string) bool {
	if strings.ContainsAny(endpoint, "\r\n\t ") {
		return false
	}
	if protocol == collectorProtocolGRPC {
		return isValidCollectorAuthority(endpoint)
	}

	parsed, err := url.Parse(endpoint)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return false
	}
	return isValidCollectorAuthority(parsed.Host)
}

func isValidCollectorAuthority(authority string) bool {
	host, port, err := net.SplitHostPort(authority)
	if err != nil || host == "" || strings.ContainsAny(host, "/@?#") {
		return false
	}
	portNumber, err := strconv.Atoi(port)
	return err == nil && portNumber > 0 && portNumber <= 65535
}

func isValidEnvironmentVariableName(name string) bool {
	for index, character := range name {
		if character == '_' || (character >= 'A' && character <= 'Z') || (character >= 'a' && character <= 'z') {
			continue
		}
		if index > 0 && character >= '0' && character <= '9' {
			continue
		}
		return false
	}
	return name != ""
}
