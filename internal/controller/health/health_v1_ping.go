package health

import (
	"context"

	"github.com/ashjazz/Longtermism/api/v1/health"
	"github.com/ashjazz/Longtermism/internal/consts"
)

// Ping 处理 GET /api/v1/health/ping。
func (c *ControllerV1) Ping(ctx context.Context, req *health.PingReq) (res *health.PingRes, err error) {
	return &health.PingRes{
		App:     consts.AppName,
		Version: consts.AppVersion,
		Ok:      true,
	}, nil
}
