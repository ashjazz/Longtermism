package cmd

import (
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

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

// ObservabilityCollectorConfigInput 是配置加载层传入的临时 DTO。
// HeaderValue 仅用于判定凭据是否被提供，绝不能被保存到运行时快照、日志或错误中。
type ObservabilityCollectorConfigInput struct {
	Endpoint      string
	Protocol      string
	Timeout       string
	Insecure      bool
	AllowInsecure bool // 生产环境使用明文传输时必须显式授权。
	HeaderEnvName string
	HeaderValue   string
}

// ObservabilityRuntimeConfigInput 是运行配置解析的窄输入边界。
type ObservabilityRuntimeConfigInput struct {
	Mode        ObservabilityRuntimeMode
	Environment string
	Collector   ObservabilityCollectorConfigInput
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
}

// ResolveObservabilityRuntimeConfig 将原始应用配置解析为不可携带 secret 的运行快照。
// 显式选择 collector 却缺少必要参数时必须启动失败，而不是静默降级并丢失观测事实。
func ResolveObservabilityRuntimeConfig(input ObservabilityRuntimeConfigInput) (ObservabilityRuntimeConfig, error) {
	mode := input.Mode
	if mode == "" {
		return ObservabilityRuntimeConfig{}, gerror.New("observability mode is required")
	}
	environment := valueOrDefault(input.Environment, defaultObservabilityEnvironment)

	switch mode {
	case ObservabilityRuntimeModeNoop, ObservabilityRuntimeModeLocal:
		return ObservabilityRuntimeConfig{Mode: mode, Environment: environment}, nil
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
		}, nil
	default:
		return ObservabilityRuntimeConfig{}, gerror.New("observability mode is unsupported")
	}
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

	timeout, err := time.ParseDuration(strings.TrimSpace(input.Timeout))
	if err != nil || timeout <= 0 || timeout > maxCollectorTimeout {
		return ObservabilityCollectorConfig{}, gerror.New("collector timeout must be positive")
	}

	if strings.EqualFold(strings.TrimSpace(environment), productionEnvironment) && input.Insecure && !input.AllowInsecure {
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
		Insecure:          input.Insecure,
		HeaderEnvName:     headerEnvName,
		CredentialPresent: strings.TrimSpace(input.HeaderValue) != "",
	}, nil
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
