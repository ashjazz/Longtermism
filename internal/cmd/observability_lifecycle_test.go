package cmd

import (
	"context"
	"errors"
	"testing"

	"github.com/jazzash/ashjazz-aiagent/pkg/ai/obs"
)

func TestObservabilityTracerProviderLifecycle(t *testing.T) {
	t.Run("initialization is idempotent", func(t *testing.T) {
		exporter := &lifecycleExporterStub{}
		lifecycle := NewObservabilityTracerProviderLifecycle(ObservabilityTracerProviderLifecycleConfig{
			Exporter: exporter,
		})

		if err := lifecycle.Initialize(context.Background()); err != nil {
			t.Fatalf("first Initialize() error = %v", err)
		}
		if err := lifecycle.Initialize(context.Background()); err != nil {
			t.Fatalf("second Initialize() error = %v", err)
		}

		if exporter.initCalls != 1 {
			t.Fatalf("exporter init calls = %d, want 1", exporter.initCalls)
		}
		if !lifecycle.Status().Initialized {
			t.Fatalf("Initialized = false, want true")
		}
	})

	t.Run("shutdown is idempotent", func(t *testing.T) {
		exporter := &lifecycleExporterStub{}
		lifecycle := NewObservabilityTracerProviderLifecycle(ObservabilityTracerProviderLifecycleConfig{
			Exporter: exporter,
		})

		if err := lifecycle.Initialize(context.Background()); err != nil {
			t.Fatalf("Initialize() error = %v", err)
		}
		if err := lifecycle.Shutdown(context.Background()); err != nil {
			t.Fatalf("first Shutdown() error = %v", err)
		}
		if err := lifecycle.Shutdown(context.Background()); err != nil {
			t.Fatalf("second Shutdown() error = %v", err)
		}

		if exporter.shutdownCalls != 1 {
			t.Fatalf("exporter shutdown calls = %d, want 1", exporter.shutdownCalls)
		}
		if !lifecycle.Status().Shutdown {
			t.Fatalf("Shutdown = false, want true")
		}
	})

	t.Run("exporter failure is captured without failing lifecycle", func(t *testing.T) {
		exporter := &lifecycleExporterStub{
			shutdownErr: errors.New("collector unavailable"),
		}
		lifecycle := NewObservabilityTracerProviderLifecycle(ObservabilityTracerProviderLifecycleConfig{
			Exporter: exporter,
		})

		if err := lifecycle.Initialize(context.Background()); err != nil {
			t.Fatalf("Initialize() error = %v", err)
		}
		if err := lifecycle.Shutdown(context.Background()); err != nil {
			t.Fatalf("Shutdown() error = %v, want exporter failure protected", err)
		}

		if lifecycle.Status().FailureStatus != string(obs.FailureTelemetryExportFailed) {
			t.Fatalf("FailureStatus = %q, want %q", lifecycle.Status().FailureStatus, obs.FailureTelemetryExportFailed)
		}
		if lifecycle.Status().FailureMessage != "collector unavailable" {
			t.Fatalf("FailureMessage = %q, want collector unavailable", lifecycle.Status().FailureMessage)
		}
	})
}

type lifecycleExporterStub struct {
	initCalls     int
	shutdownCalls int
	shutdownErr   error
}

func (s *lifecycleExporterStub) Initialize(_ context.Context) error {
	s.initCalls++
	return nil
}

func (s *lifecycleExporterStub) Shutdown(_ context.Context) error {
	s.shutdownCalls++
	return s.shutdownErr
}
