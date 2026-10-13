//go:build integration

package contract

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/leungll/Emberling/backend/internal/api"
	"github.com/leungll/Emberling/backend/internal/config/readiness"
)

type buildBody struct {
	Status  string `json:"status"`
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

func getBuildBody(t *testing.T, server *httptest.Server, path string, wantStatus int) buildBody {
	t.Helper()
	resp, body := doGet(t, server, path)
	if resp.StatusCode != wantStatus {
		t.Fatalf("GET %s status = %d, want %d, body=%s", path, resp.StatusCode, wantStatus, body)
	}
	var out buildBody
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode %s body %s: %v", path, body, err)
	}
	return out
}

// TestAPI_HealthAndReady_ReportInjectedBuild: both probes carry the build version and
// commit injected at build time, beside their unchanged status.
func TestAPI_HealthAndReady_ReportInjectedBuild(t *testing.T) {
	probe := readiness.NewProbe()
	if err := probe.Run(context.Background(), allPassingChecks(), noopObserver{}); err != nil {
		t.Fatalf("probe.Run: %v", err)
	}
	build := api.BuildInfo{Version: "v0.4.0", Commit: "2f6efb2c0ffee"}
	server := httptest.NewServer(api.NewRouter(api.Deps{Readiness: probe, Build: build}))
	defer server.Close()

	health := getBuildBody(t, server, "/health", http.StatusOK)
	if health != (buildBody{Status: "ok", Version: "v0.4.0", Commit: "2f6efb2c0ffee"}) {
		t.Errorf("GET /health = %+v", health)
	}
	ready := getBuildBody(t, server, "/ready", http.StatusOK)
	if ready != (buildBody{Status: "ready", Version: "v0.4.0", Commit: "2f6efb2c0ffee"}) {
		t.Errorf("GET /ready = %+v", ready)
	}
}

// TestAPI_HealthAndReady_NoInjectedBuild_ReportUnknown: a binary built without a version
// answers an explicit "unknown", and the missing build information never changes the
// liveness or readiness outcome.
func TestAPI_HealthAndReady_NoInjectedBuild_ReportUnknown(t *testing.T) {
	probe := readiness.NewProbe()
	server := httptest.NewServer(api.NewRouter(api.Deps{Readiness: probe}))
	defer server.Close()

	health := getBuildBody(t, server, "/health", http.StatusOK)
	if health != (buildBody{Status: "ok", Version: "unknown", Commit: "unknown"}) {
		t.Errorf("GET /health without build info = %+v", health)
	}
	if resp, body := doGet(t, server, "/ready"); resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("GET /ready before the gate status = %d, want 503, body=%s", resp.StatusCode, body)
	}

	if err := probe.Run(context.Background(), allPassingChecks(), noopObserver{}); err != nil {
		t.Fatalf("probe.Run: %v", err)
	}
	ready := getBuildBody(t, server, "/ready", http.StatusOK)
	if ready != (buildBody{Status: "ready", Version: "unknown", Commit: "unknown"}) {
		t.Errorf("GET /ready without build info = %+v", ready)
	}
}

func allPassingChecks() readiness.Checks {
	pass := func(context.Context) error { return nil }
	return readiness.Checks{
		Database: pass, Configuration: pass, Registry: pass,
		AssetStorage: pass, Reconciler: pass, FirstScan: pass,
	}
}
