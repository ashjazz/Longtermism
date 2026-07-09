package health

import (
	"context"
	"testing"

	v1 "github.com/ashjazz/Longtermism/api/v1/health"
	"github.com/ashjazz/Longtermism/internal/consts"
)

func TestControllerV1Ping(t *testing.T) {
	tests := []struct {
		name    string
		request *v1.PingReq
		want    *v1.PingRes
		wantErr bool
	}{
		{
			name:    "returns application health",
			request: &v1.PingReq{},
			want: &v1.PingRes{
				App:     consts.AppName,
				Version: consts.AppVersion,
				Ok:      true,
			},
			wantErr: false,
		},
	}

	controller := NewV1()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := controller.Ping(context.Background(), tt.request)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Ping() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got.App != tt.want.App || got.Version != tt.want.Version || got.Ok != tt.want.Ok {
				t.Fatalf("Ping() = %#v, want %#v", got, tt.want)
			}
		})
	}
}
