package cmd

import "strings"

const (
	defaultObservabilityServiceName = "longtermism"
	defaultObservabilityEnvironment = "local"

	resourceKeyServiceName           = "service.name"
	resourceKeyDeploymentEnvironment = "deployment.environment"
	resourceKeyServiceVersion        = "service.version"
	resourceKeyServiceInstanceID     = "service.instance.id"
)

// ObservabilityResourceInput 是构造基础设施观测 resource 的应用配置入口。
type ObservabilityResourceInput struct {
	ServiceName string
	Environment string
	Version     string
	InstanceID  string
}

// ObservabilityResource 是 OTel resource 语义的稳定测试快照。
//
// 真实 OTel 接入可以在 adapter 层把 Attributes 转成 resource.WithAttributes +
// semconv。这里不直接暴露 SDK 类型，是为了让 internal/cmd 的配置语义可离线测试，
// 也避免 semconv 版本升级影响调用方。
type ObservabilityResource struct {
	Attributes map[string]string
}

// BuildObservabilityResource 构造基础设施平面的服务身份属性。
func BuildObservabilityResource(input ObservabilityResourceInput) (ObservabilityResource, error) {
	attributes := map[string]string{
		resourceKeyServiceName:           valueOrDefault(input.ServiceName, defaultObservabilityServiceName),
		resourceKeyDeploymentEnvironment: valueOrDefault(input.Environment, defaultObservabilityEnvironment),
	}

	addOptionalResourceAttribute(attributes, resourceKeyServiceVersion, input.Version)
	addOptionalResourceAttribute(attributes, resourceKeyServiceInstanceID, input.InstanceID)

	return ObservabilityResource{Attributes: cloneResourceAttributes(attributes)}, nil
}

func addOptionalResourceAttribute(attributes map[string]string, key, value string) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return
	}
	attributes[key] = trimmed
}

func valueOrDefault(value, defaultValue string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return defaultValue
	}
	return trimmed
}

func cloneResourceAttributes(attributes map[string]string) map[string]string {
	cloned := make(map[string]string, len(attributes))
	for key, value := range attributes {
		cloned[key] = value
	}
	return cloned
}
