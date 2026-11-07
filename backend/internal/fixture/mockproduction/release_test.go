package mockproduction

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newRelease returns a production whose releases complete in the accepting request.
func newReleaseProduction(t *testing.T) *production {
	t.Helper()
	p := newProduction(t)
	p.server.completionDelay = 0
	return p
}

func releaseBody(operationID string) string {
	return `{"operationId":"` + operationID + `","target":{"service":"payments-api","environment":"production"},"baseCommit":"abc123","patchDigest":"sha256:feed"}`
}

func TestRelease_NewOperation_AcceptedThenDeployedAndTargetReportsIt(t *testing.T) {
	p := newReleaseProduction(t)
	p.server.PauseCompletion()

	status, body := post(t, p.srv.URL+"/v2/deployments", releaseBody("op_a_1"))
	if status != http.StatusAccepted {
		t.Fatalf("status = %d, body %s; want 202", status, body)
	}
	if got := decode[releaseResponse](t, body); got != (releaseResponse{OperationID: "op_a_1", Status: releaseAccepted}) {
		t.Errorf("response = %+v, want ACCEPTED", got)
	}
	status, body = get(t, p.srv.URL+"/v2/deployments/op_a_1")
	if got := decode[releaseResponse](t, body); status != http.StatusOK || got.Status != releaseDeploying {
		t.Fatalf("held release = %d %+v, want 200 DEPLOYING", status, got)
	}
	// A replay of the same operation reports the current status and deploys nothing new.
	status, body = post(t, p.srv.URL+"/v2/deployments", releaseBody("op_a_1"))
	if got := decode[releaseResponse](t, body); status != http.StatusOK || got.Status != releaseDeploying {
		t.Fatalf("replay = %d %+v, want 200 DEPLOYING", status, got)
	}

	p.server.ResumeCompletion()
	status, body = get(t, p.srv.URL+"/v2/deployments/op_a_1")
	if got := decode[releaseResponse](t, body); status != http.StatusOK || got != (releaseResponse{OperationID: "op_a_1", Status: releaseDeployed, Version: "v1"}) {
		t.Fatalf("completed release = %d %+v, want 200 DEPLOYED v1", status, got)
	}
	status, body = get(t, p.srv.URL+"/v2/targets/payments-api/production")
	want := releaseTargetResponse{Version: "v1", PatchDigest: "sha256:feed", Healthy: true, LastOperationID: "op_a_1"}
	if got := decode[releaseTargetResponse](t, body); status != http.StatusOK || got != want {
		t.Fatalf("target = %d %+v, want 200 %+v", status, got, want)
	}

	deployed := 0
	for _, line := range p.lines(t) {
		if line.Kind == kindRelease && line.Event == recordDeployed && line.OperationID == "op_a_1" {
			deployed++
		}
	}
	if deployed != 1 {
		t.Fatalf("deployed lines for op_a_1 = %d, want exactly 1", deployed)
	}
}

func TestRelease_UnknownOperation_Answers404(t *testing.T) {
	p := newReleaseProduction(t)
	if status, _ := get(t, p.srv.URL+"/v2/deployments/op_missing_1"); status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
	if status, _ := get(t, p.srv.URL+"/v2/targets/payments-api/production"); status != http.StatusNotFound {
		t.Fatalf("target status = %d, want 404 before any deployment", status)
	}
}

func TestRelease_VoidBeforeArrival_TombstoneRejectsTheLateRequest(t *testing.T) {
	p := newReleaseProduction(t)

	status, body := post(t, p.srv.URL+"/v2/deployments/op_late_1/void", "")
	if got := decode[releaseResponse](t, body); status != http.StatusOK || got.Status != releaseVoided {
		t.Fatalf("void of an unknown operation = %d %+v, want 200 VOIDED", status, got)
	}
	status, body = post(t, p.srv.URL+"/v2/deployments", releaseBody("op_late_1"))
	if got := decode[errorResponse](t, body); status != http.StatusConflict || got.Code != releaseVoided {
		t.Fatalf("late arrival = %d %+v, want 409 VOIDED", status, got)
	}
	// Voiding again is idempotent and the target was never touched.
	if status, _ := post(t, p.srv.URL+"/v2/deployments/op_late_1/void", ""); status != http.StatusOK {
		t.Fatalf("second void status = %d, want 200", status)
	}
	if status, _ := get(t, p.srv.URL+"/v2/targets/payments-api/production"); status != http.StatusNotFound {
		t.Fatalf("target status = %d, want 404: nothing deployed", status)
	}
	voided := 0
	for _, line := range p.lines(t) {
		if line.Event == recordVoided && line.OperationID == "op_late_1" {
			voided++
		}
	}
	if voided != 1 {
		t.Fatalf("voided lines = %d, want exactly 1 (idempotent void writes once)", voided)
	}
}

func TestRelease_VoidAcceptedBeforeStart_VoidsAndCancelsCompletion(t *testing.T) {
	p := newProduction(t)
	p.server.completionDelay = time.Hour

	post(t, p.srv.URL+"/v2/deployments", releaseBody("op_v_1"))
	status, body := post(t, p.srv.URL+"/v2/deployments/op_v_1/void", "")
	if got := decode[releaseResponse](t, body); status != http.StatusOK || got.Status != releaseVoided {
		t.Fatalf("void = %d %+v, want 200 VOIDED", status, got)
	}
	// The cancelled timer never completes it: completing by hand changes nothing either.
	p.server.mu.Lock()
	p.server.completeReleaseLocked("op_v_1")
	p.server.mu.Unlock()
	_, body = get(t, p.srv.URL+"/v2/deployments/op_v_1")
	if got := decode[releaseResponse](t, body); got.Status != releaseVoided {
		t.Fatalf("release after void = %+v, want VOIDED", got)
	}
}

func TestRelease_SetCompletionDelay_AppliesToLaterReleasesOnly(t *testing.T) {
	p := newProduction(t)
	p.server.SetCompletionDelay(time.Hour)
	post(t, p.srv.URL+"/v2/deployments", releaseBody("op_d_1"))
	p.server.SetCompletionDelay(0)
	post(t, p.srv.URL+"/v2/deployments", releaseBody("op_d_2"))

	_, body := get(t, p.srv.URL+"/v2/deployments/op_d_1")
	if got := decode[releaseResponse](t, body); got.Status != releaseAccepted {
		t.Errorf("release accepted under the hour delay = %+v, want still ACCEPTED", got)
	}
	_, body = get(t, p.srv.URL+"/v2/deployments/op_d_2")
	if got := decode[releaseResponse](t, body); got.Status != releaseDeployed {
		t.Errorf("release accepted after the delay was cleared = %+v, want DEPLOYED", got)
	}
}

func TestRelease_VoidAfterStartOrCompletion_AnswersCurrentStatus(t *testing.T) {
	p := newReleaseProduction(t)
	p.server.PauseCompletion()
	post(t, p.srv.URL+"/v2/deployments", releaseBody("op_s_1"))

	_, body := post(t, p.srv.URL+"/v2/deployments/op_s_1/void", "")
	if got := decode[releaseResponse](t, body); got.Status != releaseDeploying {
		t.Fatalf("void while DEPLOYING = %+v, want DEPLOYING", got)
	}
	p.server.ResumeCompletion()
	_, body = post(t, p.srv.URL+"/v2/deployments/op_s_1/void", "")
	if got := decode[releaseResponse](t, body); got.Status != releaseDeployed || got.Version != "v1" {
		t.Fatalf("void after DEPLOYED = %+v, want DEPLOYED v1", got)
	}
}

func TestRelease_Faults_RejectUnreachableUnhealthy(t *testing.T) {
	p := newReleaseProduction(t)

	p.server.SetReleaseFaults(ReleaseFaults{Reject: true, RejectCode: "FROZEN_WINDOW"})
	status, body := post(t, p.srv.URL+"/v2/deployments", releaseBody("op_f_1"))
	if got := decode[errorResponse](t, body); status != http.StatusConflict || got.Code != "FROZEN_WINDOW" {
		t.Fatalf("reject fault = %d %+v, want 409 FROZEN_WINDOW", status, got)
	}
	if status, _ := get(t, p.srv.URL+"/v2/deployments/op_f_1"); status != http.StatusNotFound {
		t.Fatalf("rejected operation status = %d, want 404: nothing was recorded", status)
	}

	p.server.SetReleaseFaults(ReleaseFaults{Unreachable: true})
	for _, path := range []string{"/v2/deployments/op_f_1", "/v2/targets/payments-api/production"} {
		if status, _ := get(t, p.srv.URL+path); status != http.StatusServiceUnavailable {
			t.Fatalf("GET %s while unreachable = %d, want 503", path, status)
		}
	}
	if status, _ := post(t, p.srv.URL+"/v2/deployments", releaseBody("op_f_2")); status != http.StatusServiceUnavailable {
		t.Fatalf("POST while unreachable = %d, want 503", status)
	}
	if status, _ := post(t, p.srv.URL+"/v1/deployments", deployBody("op_v1_1", "checkout", "prod", "")); status != http.StatusOK {
		t.Fatalf("v1 while v2 unreachable = %d, want 200: the fault is a v2 fault", status)
	}

	p.server.SetReleaseFaults(ReleaseFaults{Unhealthy: true})
	post(t, p.srv.URL+"/v2/deployments", releaseBody("op_f_3"))
	_, body = get(t, p.srv.URL+"/v2/targets/payments-api/production")
	if got := decode[releaseTargetResponse](t, body); got.Healthy || got.Version != "v1" || got.LastOperationID != "op_f_3" {
		t.Fatalf("target after unhealthy completion = %+v, want unhealthy v1 by op_f_3", got)
	}
	health := 0
	for _, line := range p.lines(t) {
		if line.Event == recordHealth && line.Version == "v1" {
			health++
		}
	}
	if health != 1 {
		t.Fatalf("health lines = %d, want 1", health)
	}
}

func TestRelease_DropResponse_AcceptsWithoutAnswering(t *testing.T) {
	p := newReleaseProduction(t)
	p.server.SetReleaseFaults(ReleaseFaults{DropResponse: true})

	_, err := http.Post(p.srv.URL+"/v2/deployments", "application/json", strings.NewReader(releaseBody("op_d_1")))
	if err == nil {
		t.Fatal("POST with dropResponse: error = nil, want a closed connection")
	}
	// The release was accepted and completed all the same.
	status, body := get(t, p.srv.URL+"/v2/deployments/op_d_1")
	if got := decode[releaseResponse](t, body); status != http.StatusOK || got.Status != releaseDeployed {
		t.Fatalf("release after dropped answer = %d %+v, want 200 DEPLOYED", status, got)
	}
	dropped := 0
	for _, line := range p.lines(t) {
		if line.Event == recordResponseDropped && line.OperationID == "op_d_1" {
			dropped++
		}
	}
	if dropped != 1 {
		t.Fatalf("responseDropped lines = %d, want 1", dropped)
	}
}

func TestRelease_Restart_ReplaysDeployedVoidedAndHealthFromRecord(t *testing.T) {
	recordPath := filepath.Join(t.TempDir(), "record.jsonl")
	record, err := OpenRecord(recordPath)
	if err != nil {
		t.Fatalf("OpenRecord() error = %v", err)
	}
	first := NewServer(WithTestControls(record), WithCompletionDelay(0))
	first.now = func() time.Time { return fixedNow }
	srv := httptest.NewServer(first)
	first.SetReleaseFaults(ReleaseFaults{Unhealthy: true})
	post(t, srv.URL+"/v2/deployments", releaseBody("op_r_1"))
	post(t, srv.URL+"/v2/deployments/op_r_2/void", "")
	first.StopHolding()
	srv.Close()
	if err := record.Close(); err != nil {
		t.Fatalf("close record: %v", err)
	}

	reopened, err := OpenRecord(recordPath)
	if err != nil {
		t.Fatalf("reopen record: %v", err)
	}
	second := NewServer(WithTestControls(reopened), WithCompletionDelay(0))
	srv2 := httptest.NewServer(second)
	t.Cleanup(func() { second.StopHolding(); srv2.Close(); _ = reopened.Close() })

	status, body := get(t, srv2.URL+"/v2/deployments/op_r_1")
	if got := decode[releaseResponse](t, body); status != http.StatusOK || got != (releaseResponse{OperationID: "op_r_1", Status: releaseDeployed, Version: "v1"}) {
		t.Fatalf("replayed release = %d %+v, want DEPLOYED v1", status, got)
	}
	status, body = post(t, srv2.URL+"/v2/deployments", releaseBody("op_r_2"))
	if status != http.StatusConflict {
		t.Fatalf("arrival of a voided operation after restart = %d, body %s; want 409", status, body)
	}
	_, body = get(t, srv2.URL+"/v2/targets/payments-api/production")
	if got := decode[releaseTargetResponse](t, body); got.Version != "v1" || got.Healthy || got.LastOperationID != "op_r_1" {
		t.Fatalf("replayed target = %+v, want unhealthy v1 by op_r_1", got)
	}
	// The version counter continues after the replayed one.
	post(t, srv2.URL+"/v2/deployments", releaseBody("op_r_3"))
	_, body = get(t, srv2.URL+"/v2/deployments/op_r_3")
	if got := decode[releaseResponse](t, body); got.Version != "v2" {
		t.Fatalf("next release after replay = %+v, want v2", got)
	}
}

func TestRelease_Barrier_HeldReleaseIsUnknownUntilReleased(t *testing.T) {
	p := newReleaseProduction(t)
	if status, _ := post(t, p.srv.URL+"/control/pause", `{"kinds":["release"]}`); status != http.StatusOK {
		t.Fatalf("pause status = %d", status)
	}
	done := make(chan int, 1)
	go func() {
		status, _ := post(t, p.srv.URL+"/v2/deployments", releaseBody("op_h_1"))
		done <- status
	}()
	select {
	case got := <-p.held:
		if got != "op_h_1" {
			t.Fatalf("held = %q, want op_h_1", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the release was not held")
	}
	if status, _ := get(t, p.srv.URL+"/v2/deployments/op_h_1"); status != http.StatusNotFound {
		t.Fatalf("held release status = %d, want 404: production has not accepted it", status)
	}
	// A /v1/ deploy is not held by a release-only pause.
	if status, _ := post(t, p.srv.URL+"/v1/deployments", deployBody("op_v1_h", "checkout", "prod", "")); status != http.StatusOK {
		t.Fatalf("v1 deploy during release pause = %d, want 200", status)
	}
	if status, _ := post(t, p.srv.URL+"/control/release", ""); status != http.StatusOK {
		t.Fatalf("release status = %d", status)
	}
	if status := <-done; status != http.StatusAccepted {
		t.Fatalf("released POST status = %d, want 202", status)
	}
}

func TestRelease_ControlRoutes_SetFaultsAndCompletionOverHTTP(t *testing.T) {
	p := newReleaseProduction(t)
	if status, _ := post(t, p.srv.URL+"/control/v2/completion", `{"paused":true}`); status != http.StatusOK {
		t.Fatalf("pause completion status = %d", status)
	}
	post(t, p.srv.URL+"/v2/deployments", releaseBody("op_c_1"))
	_, body := get(t, p.srv.URL+"/v2/deployments/op_c_1")
	if got := decode[releaseResponse](t, body); got.Status != releaseDeploying {
		t.Fatalf("release with completion paused over HTTP = %+v, want DEPLOYING", got)
	}
	if status, _ := post(t, p.srv.URL+"/control/v2/faults", `{"reject":true}`); status != http.StatusOK {
		t.Fatalf("faults status = %d", status)
	}
	if status, _ := post(t, p.srv.URL+"/v2/deployments", releaseBody("op_c_2")); status != http.StatusConflict {
		t.Fatalf("rejected over HTTP = %d, want 409", status)
	}
	post(t, p.srv.URL+"/control/v2/faults", "")
	post(t, p.srv.URL+"/control/v2/completion", `{"paused":false}`)
	_, body = get(t, p.srv.URL+"/v2/deployments/op_c_1")
	if got := decode[releaseResponse](t, body); got.Status != releaseDeployed {
		t.Fatalf("release after resume = %+v, want DEPLOYED", got)
	}
}

func TestServer_ControlsDisabled_ReleaseFaultsIgnoredAndNoControlRoutes(t *testing.T) {
	server := NewServer(WithCompletionDelay(0))
	srv := httptest.NewServer(server)
	defer srv.Close()
	server.SetReleaseFaults(ReleaseFaults{Reject: true, Unreachable: true})
	if status, _ := post(t, srv.URL+"/v2/deployments", releaseBody("op_n_1")); status != http.StatusAccepted {
		t.Fatalf("release without controls = %d, want 202: faults need test controls", status)
	}
	if status, _ := post(t, srv.URL+"/control/v2/faults", `{}`); status != http.StatusNotFound {
		t.Fatalf("control route without controls = %d, want 404", status)
	}
}
