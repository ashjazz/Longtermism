package backend

import (
	"strings"
	"testing"
	"time"

	"github.com/ashjazz/Longtermism/internal/observability/smoke"
)

// Production composition must not resolve a manifest from one contained root and read its
// registered artifacts through another. Pointer identity is intentional here: equal path text
// cannot prove that two capabilities are bound to the same already-validated directory FD.
func TestPrivacySmokeBackendRejectsSplitBrainArtifactStores(t *testing.T) {
	storeA, err := smoke.OpenPrivacyArtifactStore(t.TempDir())
	if err != nil {
		t.Fatalf("open store A: %v", err)
	}
	t.Cleanup(func() { _ = storeA.Close() })
	storeB, err := smoke.OpenPrivacyArtifactStore(t.TempDir())
	if err != nil {
		t.Fatalf("open store B: %v", err)
	}
	t.Cleanup(func() { _ = storeB.Close() })

	local, err := NewPrivacyLocalSurfaces(PrivacyLocalSurfacesConfig{
		RuntimeConfigDigest:            "sha256:" + strings.Repeat("a", 64),
		ExpectedPrequeueArtifactSHA256: "sha256:" + strings.Repeat("d", 64),
		CollectorComponent:             "otlphttp/loki",
		ExportAdmissionCorrelation:     "admission-t192",
	}, storeA)
	if err != nil {
		t.Fatalf("construct local surfaces: %v", err)
	}

	if backend, err := NewPrivacySmokeBackend(storeB, local, &PrivacyGrafanaSurfaces{}, &PrivacyLangfuseSurfaces{}, time.Second); backend != nil || t192BackendClass(err) != "artifact_store_mismatch" {
		t.Fatal("production backend accepted manifest and artifact capabilities from different stores")
	}
	if _, err := NewPrivacySmokeBackend(storeA, local, &PrivacyGrafanaSurfaces{}, &PrivacyLangfuseSurfaces{}, time.Second); t192BackendClass(err) == "artifact_store_mismatch" {
		t.Fatal("production backend rejected the exact store capability held by local surfaces")
	}
}

func t192BackendClass(err error) string {
	type classified interface{ Class() string }
	if value, ok := err.(classified); ok {
		return value.Class()
	}
	if err == nil {
		return ""
	}
	return err.Error()
}
