//go:build integration

// This file proves that the Mock Provider path and the task gateway path coexist in one
// Backend and never cross: each image model's tasks go to the Provider its registration
// names, each Callback Binding records that Provider, and a status query for a task goes
// to the Provider that accepted it. Both Providers run in process and deliver callbacks
// to the real POST /api/callbacks intake; the gateway reads the Run's reference image
// through the real Asset content route.
package contract

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/adapters/mocktask"
	"github.com/leungll/Emberling/backend/internal/fixture/mockprovider"
	"github.com/leungll/Emberling/backend/internal/fixture/taskgateway"
	"github.com/leungll/Emberling/backend/internal/nodes/imagegeneration"
	"github.com/leungll/Emberling/backend/internal/registry"
)

const (
	testGatewayProviderID = "task-gateway-v1"
	testGatewayModelID    = "gateway-test-image"
)

// requestLog records "METHOD path" for every request one server receives.
type requestLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *requestLog) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l.mu.Lock()
		l.lines = append(l.lines, r.Method+" "+r.URL.Path)
		l.mu.Unlock()
		next.ServeHTTP(w, r)
	})
}

func (l *requestLog) matching(prefix string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, line := range l.lines {
		if strings.HasPrefix(line, prefix) {
			out = append(out, line)
		}
	}
	return out
}

// retargetTransport sends every request to the current target origin, so a fixture
// built before the Backend can reach the Backend's httptest server.
type retargetTransport struct {
	mu     sync.Mutex
	target string
}

func (rt *retargetTransport) set(origin string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.target = origin
}

func (rt *retargetTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.mu.Lock()
	target := rt.target
	rt.mu.Unlock()
	origin, err := url.Parse(target)
	if err != nil {
		return nil, err
	}
	out := req.Clone(req.Context())
	out.URL.Scheme, out.URL.Host, out.Host = origin.Scheme, origin.Host, origin.Host
	return http.DefaultTransport.RoundTrip(out)
}

// gatewayGraphRequest is the gateway's demo shape: a brief through a prompt template and
// a reference photo into image_generation, whose image reaches the output.
func gatewayGraphRequest(modelID string) map[string]any {
	return map[string]any{
		"name":        "Gateway Image Generation",
		"description": "",
		"nodes": []map[string]any{
			{"id": "brief", "type": "text_input", "config": map[string]any{"inputKey": "brief", "required": true}},
			{"id": "reference", "type": "image_input", "config": map[string]any{
				"inputKey": "reference", "required": true,
				"acceptedMediaTypes": []string{"image/png"}, "maxSizeBytes": float64(1 << 20),
			}},
			{"id": "prompt", "type": "prompt_template", "config": map[string]any{"template": "{{text}}"}},
			{"id": "image", "type": "image_generation", "config": map[string]any{"modelId": modelID, "width": float64(1024)}},
			{"id": "output", "type": "media_output", "config": map[string]any{}},
		},
		"edges": []map[string]any{
			{"id": "e1", "source": "brief", "sourceHandle": "text", "target": "prompt", "targetHandle": "text"},
			{"id": "e2", "source": "prompt", "sourceHandle": "text", "target": "image", "targetHandle": "prompt"},
			{"id": "e3", "source": "reference", "sourceHandle": "image", "target": "image", "targetHandle": "reference"},
			{"id": "e4", "source": "image", "sourceHandle": "image", "target": "output", "targetHandle": "image"},
			{"id": "e5", "source": "brief", "sourceHandle": "text", "target": "output", "targetHandle": "caption"},
		},
	}
}

// createRunFor saves definition and starts one Run with input.
func (e *testEnv) createRunFor(t *testing.T, definition, input map[string]any) string {
	t.Helper()
	resp, body := e.doJSON(t, http.MethodPost, "/api/definitions", definition)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/definitions status = %d, body=%s", resp.StatusCode, body)
	}
	created := decodeBody[map[string]any](t, body)
	resp, body = e.doJSON(t, http.MethodPost, "/api/runs", map[string]any{
		"workflowId": created["workflowId"], "definitionVersion": created["version"], "input": input,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/runs status = %d, body=%s", resp.StatusCode, body)
	}
	runID, _ := decodeBody[map[string]any](t, body)["id"].(string)
	return runID
}

// imageBindingOf returns the Callback Binding of a completed Run's image NodeRun.
func (e *testEnv) imageBindingOf(t *testing.T, runID string) map[string]any {
	t.Helper()
	nodeRun := e.nodeRunOf(t, runID, "image")
	nodeRunID, _ := nodeRun["id"].(string)
	detail, _ := e.nodeRunDetail(t, runID, nodeRunID)
	attempts := attemptsOf(t, detail)
	binding := bindingOf(attempts[len(attempts)-1])
	if binding == nil {
		t.Fatalf("Run %s image Attempt projects no Callback Binding", runID)
	}
	return binding
}

func TestImageGeneration_GatewayAndMockModels_DispatchToOwnProviderOnly(t *testing.T) {
	// The Mock Provider, delivering callbacks to the Backend once it exists.
	mockLog := &requestLog{}
	mockCallbacks := &retargetTransport{}
	mockDispatcher := mockprovider.NewDispatcher(&http.Client{Transport: mockCallbacks, Timeout: e2eHTTPTimeout})
	mockServer := httptest.NewServer(mockLog.wrap(mockprovider.NewServer(mockDispatcher)))

	// The gateway, whose upstream is the real Images API Adapter against a Mock Provider,
	// reading Asset content and delivering callbacks through the Backend once it exists.
	gatewayLog := &requestLog{}
	backend := &retargetTransport{}
	gateway, err := taskgateway.NewServer(taskgateway.Config{
		Models:          map[string]taskgateway.ImageGenerator{testGatewayModelID: gatewayImageGenerator{provider: newMockImagesAPIProvider(t, testGatewayModelID)}},
		AssetBaseURL:    "http://emberling.contract-test.invalid",
		PublicBaseURL:   "http://gateway.contract-test.invalid",
		Workers:         2,
		QueueSize:       8,
		MaxTaskDuration: time.Minute,
		MaxImageBytes:   1 << 20,
		ImageStoreBytes: 4 << 20,
		ReferenceClient: &http.Client{Transport: backend, Timeout: e2eHTTPTimeout},
		CallbackClient:  &http.Client{Transport: backend, Timeout: e2eHTTPTimeout},
	})
	if err != nil {
		t.Fatalf("taskgateway.NewServer() error = %v", err)
	}
	gatewayServer := httptest.NewServer(gatewayLog.wrap(gateway))
	t.Cleanup(func() {
		mockServer.Close()
		gatewayServer.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = mockDispatcher.Shutdown(ctx)
		_ = gateway.Shutdown(ctx)
	})

	env := newTestEnvWithOptions(t, testEnvOptions{
		MockTaskBaseURL: mockServer.URL,
		TaskClient:      &http.Client{Timeout: e2eHTTPTimeout},
		TaskGateway: &testTaskGateway{
			ProviderID: testGatewayProviderID,
			BaseURL:    gatewayServer.URL,
			Client:     &http.Client{Timeout: e2eHTTPTimeout},
			Models:     []mocktask.TaskModel{{ID: testGatewayModelID, DisplayName: "Gateway Test Image"}},
		},
	})
	mockCallbacks.set(env.server.URL)
	backend.set(env.server.URL)

	reference := env.uploadPNG(t, protocolReferencePNG(t))
	gatewayRun := env.createRunFor(t, gatewayGraphRequest(testGatewayModelID), map[string]any{"brief": "a small ember creature", "reference": reference})
	mockRun := env.createAIGCRun(t, "a small ember creature")

	for _, runID := range []string{gatewayRun, mockRun} {
		snap := env.waitForTerminal(t, runID, e2eWait)
		run, _ := snap["run"].(map[string]any)
		if status, _ := run["status"].(string); status != "COMPLETED" {
			t.Fatalf("Run %s status = %q, want COMPLETED; snapshot=%v", runID, status, snap)
		}
	}

	gatewayBinding := env.imageBindingOf(t, gatewayRun)
	mockBinding := env.imageBindingOf(t, mockRun)
	if got, _ := gatewayBinding["providerId"].(string); got != testGatewayProviderID {
		t.Fatalf("gateway Run Binding providerId = %q, want %q", got, testGatewayProviderID)
	}
	if got, _ := mockBinding["providerId"].(string); got != mocktask.ProviderID {
		t.Fatalf("mock Run Binding providerId = %q, want %q", got, mocktask.ProviderID)
	}
	gatewayTaskID, _ := gatewayBinding["externalTaskId"].(string)
	mockTaskID, _ := mockBinding["externalTaskId"].(string)
	if !strings.HasPrefix(gatewayTaskID, "gw_") || strings.HasPrefix(mockTaskID, "gw_") {
		t.Fatalf("external task ids = %q (gateway), %q (mock); want only the gateway's to carry gw_", gatewayTaskID, mockTaskID)
	}

	// Each Provider received exactly its own task.
	if got := gatewayLog.matching("POST /v1/tasks"); len(got) != 1 {
		t.Fatalf("gateway task dispatches = %v, want exactly one", got)
	}
	if got := mockLog.matching("POST /v1/tasks"); len(got) != 1 {
		t.Fatalf("mock provider task dispatches = %v, want exactly one", got)
	}

	// A status query for each Binding goes to the Provider that accepted the task. The
	// contract harness runs no Reconciler, so the registered node's Poll is called here
	// directly with each Binding, through the same two task instances the Backend has.
	poller := imagegeneration.Registration(nil,
		mocktask.New(mockServer.URL, nil),
		mocktask.New(gatewayServer.URL, nil, mocktask.WithProviderID(testGatewayProviderID), mocktask.WithoutMockScenarios()),
	).Binding.(registry.ExecutorBinding).Executor.(registry.PollableAsyncNodeExecutor)
	for _, binding := range []map[string]any{gatewayBinding, mockBinding} {
		providerID, _ := binding["providerId"].(string)
		externalTaskID, _ := binding["externalTaskId"].(string)
		result, err := poller.Poll(context.Background(), registry.NodeAsyncState{ExternalTask: registry.ExternalTask{ProviderID: providerID, ExternalTaskID: externalTaskID}})
		if err != nil || result.Status != registry.PollSucceeded {
			t.Fatalf("Poll(%s %s) = %+v, %v; want SUCCEEDED from its own Provider", providerID, externalTaskID, result, err)
		}
	}
	if got := gatewayLog.matching("GET /v1/tasks/"); len(got) != 1 || got[0] != "GET /v1/tasks/"+gatewayTaskID {
		t.Fatalf("gateway status queries = %v, want only its own task", got)
	}
	if got := mockLog.matching("GET /v1/tasks/"); len(got) != 1 || got[0] != "GET /v1/tasks/"+mockTaskID {
		t.Fatalf("mock provider status queries = %v, want only its own task", got)
	}

	// The gateway Run's output is the gateway's EXTERNAL image.
	imageRun := env.nodeRunOf(t, gatewayRun, "image")
	output, _ := imageRun["output"].(map[string]any)
	image, _ := output["image"].(map[string]any)
	if uri, _ := image["uri"].(string); image["source"] != "EXTERNAL" || !strings.HasPrefix(uri, "http://gateway.contract-test.invalid/v1/images/img_") {
		t.Fatalf("gateway image output = %v, want the gateway's EXTERNAL image", output)
	}
}
