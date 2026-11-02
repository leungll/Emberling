package taskgateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestServer_Succeeded_CallbackCarriesExternalImageAndCost(t *testing.T) {
	f := newGatewayFixture(t, fixtureOptions{})
	status, raw := f.post(t, f.taskBody("nr_1", f.references.assetRef(f.references.content)))
	if status != http.StatusAccepted {
		t.Fatalf("POST /v1/tasks status = %d, want 202; body=%s", status, raw)
	}
	id := externalTaskIDOf(t, raw)
	if !strings.HasPrefix(id, "gw_") {
		t.Fatalf("externalTaskId = %q, want the gw_ prefix", id)
	}

	got := f.callbacks.next(t)
	if got.token != testToken {
		t.Fatalf("callback token header = %q, want the dispatched token", got.token)
	}
	callbackID, payload, rawPayload := decodedCallback(t, got.body)
	if callbackID != id || payload.Status != statusSucceeded || payload.Error != nil {
		t.Fatalf("callback = %s, want a SUCCEEDED payload for %s", got.body, id)
	}
	if payload.Image == nil || payload.Image.Source != "EXTERNAL" || payload.Image.MediaType != "image/png" ||
		!strings.HasPrefix(payload.Image.URI, "http://gateway.public.test:9104/v1/images/img_") {
		t.Fatalf("callback image = %+v, want an EXTERNAL image under the public URL", payload.Image)
	}
	if payload.Usage == nil || payload.Usage.Cost != 0.04 {
		t.Fatalf("callback usage = %+v, want the upstream cost 0.04", payload.Usage)
	}
	var members map[string]json.RawMessage
	_ = json.Unmarshal(rawPayload, &members)
	var imageMembers map[string]any
	_ = json.Unmarshal(members["image"], &imageMembers)
	if len(imageMembers) != 3 {
		t.Fatalf("callback image members = %v, want only source, uri and mediaType", imageMembers)
	}

	// The staged image is served at the reported path, and the status payload is the
	// callback payload byte for byte.
	path := strings.TrimPrefix(payload.Image.URI, "http://gateway.public.test:9104")
	imageStatus, content := f.get(t, path)
	if imageStatus != http.StatusOK || !bytes.Equal(content, f.references.content) {
		t.Fatalf("GET %s = %d with %d bytes, want 200 with the generated image", path, imageStatus, len(content))
	}
	statusCode, statusRaw := f.get(t, "/v1/tasks/"+id)
	var answer taskStatusResponse
	if err := json.Unmarshal(statusRaw, &answer); err != nil || statusCode != http.StatusOK {
		t.Fatalf("GET /v1/tasks/%s = %d %s", id, statusCode, statusRaw)
	}
	if answer.Status != statusSucceeded || !bytes.Equal(answer.Payload, rawPayload) {
		t.Fatalf("status payload = %s, want the callback payload %s", answer.Payload, rawPayload)
	}

	// The upstream received the prompt and the verified reference bytes, nothing else.
	f.generator.mu.Lock()
	request := f.generator.requests[0]
	f.generator.mu.Unlock()
	if request.Prompt != testPrompt || !bytes.Equal(request.ReferenceImage, f.references.content) || request.ReferenceMediaType != "image/png" {
		t.Fatalf("upstream request = %+v, want the prompt and the reference image", request)
	}
}

func TestServer_UpstreamOmitsCost_CallbackOmitsUsage(t *testing.T) {
	generator := &fakeGenerator{image: GeneratedImage{MediaType: "image/png", Content: testPNG(t)}}
	f := newGatewayFixture(t, fixtureOptions{generator: generator})
	status, raw := f.post(t, f.taskBody("", map[string]any{"source": "EXTERNAL", "uri": f.references.server.URL + "/external/photo.png", "mediaType": "image/png"}))
	if status != http.StatusAccepted {
		t.Fatalf("POST /v1/tasks status = %d; body=%s", status, raw)
	}
	_, payload, rawPayload := decodedCallback(t, f.callbacks.next(t).body)
	if payload.Status != statusSucceeded {
		t.Fatalf("callback payload = %s, want SUCCEEDED", rawPayload)
	}
	if strings.Contains(string(rawPayload), "usage") {
		t.Fatalf("callback payload = %s, want no usage member when the upstream reports no cost", rawPayload)
	}
}

func TestServer_Validation_Rejects400WithoutClaimingKey(t *testing.T) {
	f := newGatewayFixture(t, fixtureOptions{})
	valid := func() map[string]any { return f.taskBody("nr_v", f.references.assetRef(f.references.content)) }
	cases := map[string]func(map[string]any){
		"missing reference":      func(b map[string]any) { delete(b, "reference") },
		"unknown model":          func(b map[string]any) { b["options"] = map[string]any{"modelId": "image-model-v1"} },
		"missing callback url":   func(b map[string]any) { delete(b, "callbackUrl") },
		"missing callback token": func(b map[string]any) { delete(b, "callbackToken") },
		"unsupported source":     func(b map[string]any) { b["reference"] = map[string]any{"source": "ARTIFACT"} },
		"invalid deadline":       func(b map[string]any) { b["deadline"] = "tomorrow" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			body := valid()
			mutate(body)
			status, raw := f.post(t, body)
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", status, raw)
			}
			mustNotContain(t, "400 body", string(raw), testToken, testPrompt)
		})
	}
	// None of the refusals claimed the key, so the valid dispatch is a new task.
	status, raw := f.post(t, valid())
	if status != http.StatusAccepted {
		t.Fatalf("valid dispatch after refusals: status = %d, want 202; body=%s", status, raw)
	}
	if calls := f.generator.calls.Load(); calls > 1 {
		t.Fatalf("upstream calls = %d, want at most 1", calls)
	}
}

func TestServer_ConcurrentSameKey_CreatesOneTaskAndOneUpstreamCall(t *testing.T) {
	f := newGatewayFixture(t, fixtureOptions{})
	body := f.taskBody("nr_same", f.references.assetRef(f.references.content))

	start := make(chan struct{})
	type answer struct {
		status int
		raw    []byte
	}
	answers := make(chan answer, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			status, raw := f.post(t, body)
			answers <- answer{status, raw}
		}()
	}
	close(start)
	wg.Wait()
	close(answers)

	var ids []string
	statuses := map[int]int{}
	for a := range answers {
		statuses[a.status]++
		ids = append(ids, externalTaskIDOf(t, a.raw))
	}
	if statuses[http.StatusAccepted] != 1 || statuses[http.StatusOK] != 1 || ids[0] != ids[1] {
		t.Fatalf("answers = %v with ids %v, want one 202 and one 200 naming the same task", statuses, ids)
	}
	f.callbacks.next(t)

	// A third replay after completion still names the original task.
	status, raw := f.post(t, body)
	if status != http.StatusOK || externalTaskIDOf(t, raw) != ids[0] {
		t.Fatalf("replay after completion = %d %s, want 200 with %s", status, raw, ids[0])
	}
	if calls := f.generator.calls.Load(); calls != 1 {
		t.Fatalf("upstream calls = %d, want exactly 1", calls)
	}
	if n := f.callbacks.count(); n != 1 {
		t.Fatalf("callbacks = %d, want exactly 1", n)
	}
}

func TestServer_QueueFull_Rejects429WithoutClaimingKey(t *testing.T) {
	generator := &fakeGenerator{
		entered: make(chan struct{}, 4),
		gate:    make(chan struct{}),
		image:   GeneratedImage{MediaType: "image/png", Content: testPNG(t)},
	}
	f := newGatewayFixture(t, fixtureOptions{generator: generator, workers: 1, queueSize: 1})
	ref := f.references.assetRef(f.references.content)

	if status, raw := f.post(t, f.taskBody("nr_a", ref)); status != http.StatusAccepted {
		t.Fatalf("first dispatch = %d %s", status, raw)
	}
	<-generator.entered // the only worker is busy with the first task
	if status, raw := f.post(t, f.taskBody("nr_b", ref)); status != http.StatusAccepted {
		t.Fatalf("second dispatch = %d %s, want it queued", status, raw)
	}
	status, raw := f.post(t, f.taskBody("nr_c", ref))
	if status != http.StatusTooManyRequests {
		t.Fatalf("third dispatch = %d %s, want 429 with the queue full", status, raw)
	}

	close(generator.gate)
	f.callbacks.next(t)
	f.callbacks.next(t)
	// The refused key was released: the same key is now a new task, not a replay.
	status, raw = f.post(t, f.taskBody("nr_c", ref))
	if status != http.StatusAccepted {
		t.Fatalf("retry of the refused key = %d %s, want 202 for a new task", status, raw)
	}
}

func TestServer_UnknownTask_Returns404(t *testing.T) {
	f := newGatewayFixture(t, fixtureOptions{})
	if status, raw := f.get(t, "/v1/tasks/gw_unknown"); status != http.StatusNotFound {
		t.Fatalf("GET unknown task = %d %s, want 404", status, raw)
	}
	if status, _ := f.get(t, "/v1/images/img_unknown"); status != http.StatusNotFound {
		t.Fatalf("GET unknown image = %d, want 404", status)
	}
	if status, raw := f.get(t, "/healthz"); status != http.StatusOK || !strings.Contains(string(raw), `"ok"`) {
		t.Fatalf("GET /healthz = %d %s", status, raw)
	}
}

func TestServer_RequestDeadline_BoundsTheTask(t *testing.T) {
	f := newGatewayFixture(t, fixtureOptions{})
	body := f.taskBody("", f.references.assetRef(f.references.content))
	// A deadline already behind the gateway's clock expires the task before any call.
	body["deadline"] = testNow.Add(-time.Second).Format(time.RFC3339)
	if status, raw := f.post(t, body); status != http.StatusAccepted {
		t.Fatalf("POST = %d %s", status, raw)
	}
	_, payload, raw := decodedCallback(t, f.callbacks.next(t).body)
	if payload.Status != statusFailed || payload.Error == nil || payload.Error.Code != codeTaskExpired {
		t.Fatalf("payload = %s, want FAILED with %s", raw, codeTaskExpired)
	}
	if calls := f.generator.calls.Load(); calls != 0 {
		t.Fatalf("upstream calls = %d, want none after the deadline", calls)
	}
}

func TestServer_Credentials_AbsentFromCallbackStatusLogsAndErrors(t *testing.T) {
	type path struct {
		name      string
		generator *fakeGenerator
		reference func(f *gatewayFixture) any
	}
	paths := []path{
		{"success", nil, func(f *gatewayFixture) any { return f.references.assetRef(f.references.content) }},
		{"upstream failure", &fakeGenerator{err: errUpstream}, func(f *gatewayFixture) any { return f.references.assetRef(f.references.content) }},
		{"reference failure", nil, func(f *gatewayFixture) any {
			return map[string]any{"source": "EXTERNAL", "uri": f.references.server.URL + "/broken", "mediaType": "image/png"}
		}},
	}
	for _, p := range paths {
		t.Run(p.name, func(t *testing.T) {
			f := newGatewayFixture(t, fixtureOptions{generator: p.generator})
			status, raw := f.post(t, f.taskBody("nr_cred", p.reference(f)))
			if status != http.StatusAccepted {
				t.Fatalf("POST = %d %s", status, raw)
			}
			id := externalTaskIDOf(t, raw)
			got := f.callbacks.next(t)
			_, statusRaw := f.get(t, "/v1/tasks/"+id)

			secrets := []string{testToken, testAPIKey, testPrompt, f.callbacks.server.URL}
			mustNotContain(t, "dispatch answer", string(raw), secrets...)
			mustNotContain(t, "callback body", string(got.body), secrets...)
			mustNotContain(t, "status answer", string(statusRaw), secrets...)

			ctx, cancel := context.WithTimeout(context.Background(), testWait)
			defer cancel()
			if err := f.server.Shutdown(ctx); err != nil {
				t.Fatalf("Shutdown() error = %v", err)
			}
			logs := f.logs.String()
			if !strings.Contains(logs, id) {
				t.Fatalf("logs do not mention the task %s: %s", id, logs)
			}
			mustNotContain(t, "logs", logs, secrets...)
			// The reference bytes are not logged either.
			mustNotContain(t, "logs", logs, string(f.references.content[:8]))
		})
	}
}

func TestServer_Shutdown_StopsWorkersAndDeliveries(t *testing.T) {
	generator := &fakeGenerator{entered: make(chan struct{}, 1), gate: make(chan struct{})}
	f := newGatewayFixture(t, fixtureOptions{generator: generator, workers: 1, queueSize: 2})
	ref := f.references.assetRef(f.references.content)
	running, _ := f.post(t, f.taskBody("nr_running", ref))
	<-generator.entered
	queued, raw := f.post(t, f.taskBody("nr_queued", ref))
	if running != http.StatusAccepted || queued != http.StatusAccepted {
		t.Fatalf("dispatches = %d, %d (%s), want both accepted", running, queued, raw)
	}
	queuedID := externalTaskIDOf(t, raw)

	ctx, cancel := context.WithTimeout(context.Background(), testWait)
	defer cancel()
	if err := f.server.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	// The running task was interrupted, the queued one never started, and no callback was
	// delivered for either.
	if calls := generator.calls.Load(); calls != 1 {
		t.Fatalf("upstream calls = %d, want only the interrupted one", calls)
	}
	if n := f.callbacks.count(); n != 0 {
		t.Fatalf("callbacks after shutdown = %d, want none", n)
	}
	_, statusRaw := f.get(t, "/v1/tasks/"+queuedID)
	if !strings.Contains(string(statusRaw), statusRunning) {
		t.Fatalf("queued task status = %s, want it left RUNNING", statusRaw)
	}
	// A delivery handed over after shutdown is dropped.
	f.server.dispatcher.deliver(delivery{externalTaskID: "gw_late", url: f.callbacks.server.URL, deadline: testNow.Add(time.Hour)})
	if n := f.callbacks.count(); n != 0 {
		t.Fatalf("callbacks after shutdown = %d, want none", n)
	}
}
