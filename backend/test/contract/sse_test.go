//go:build integration

// Package contract: SSE-specific tests. These use newTestEnvWithOptions's barrier and
// dropNotifier hooks (harness_test.go) to drive the two handoff windows CLAUDE.md calls
// out as non-negotiable: Snapshot-to-SSE (no gap, no duplicate across
// the boundary) and reconnect-via-Last-Event-ID (resume without duplicates), plus the
// notifier-wakes-cursor / poll-ticker-fallback pair that proves the stream never treats
// the in-process notification itself as the Event source.
package contract

import (
	"bufio"
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/api"
	"github.com/leungll/Emberling/backend/internal/service"
)

const sseTestDocument = "Emberling is a durable agent runtime."

// createAndBarrierRun POSTs a Run against the document_processing fixture with the given
// env's provider stalled by b (b must already be installed as env.provider.BeforeReturn
// before this is called), and blocks until the barrier has been entered - i.e. until
// execution has genuinely stalled mid-Run, not merely "probably by now".
func createAndBarrierRun(t *testing.T, env *testEnv, b *barrier) (runID string) {
	t.Helper()
	workflowID, version := createFixtureDefinition(t, env)
	_, body := env.doJSON(t, http.MethodPost, "/api/runs", map[string]any{
		"workflowId": workflowID, "definitionVersion": version,
		"input": map[string]any{"document": sseTestDocument},
	})
	created := decodeBody[map[string]any](t, body)
	runID, _ = created["id"].(string)
	if runID == "" {
		t.Fatalf("POST /api/runs response missing id: %s", body)
	}
	b.waitEntered(t, 5*time.Second)
	return runID
}

// snapshotLastSeq GETs the Run's current Snapshot and returns its top-level lastSeq.
func snapshotLastSeq(t *testing.T, env *testEnv, runID string) int64 {
	t.Helper()
	resp, body := env.doJSON(t, http.MethodGet, "/api/runs/"+runID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/runs/%s status = %d, body=%s", runID, resp.StatusCode, body)
	}
	snap := decodeBody[map[string]any](t, body)
	lastSeqF, _ := snap["lastSeq"].(float64)
	return int64(lastSeqF)
}

// TestAPI_Snapshot_ThenStreamFromLastSeq_NoGapNoDuplicate covers the Snapshot-to-SSE
// handoff window: a client that GETs a Snapshot and then opens SSE with
// afterSeq=snapshot.lastSeq must see exactly the Events committed after that Snapshot was
// taken - no gap (a commit that landed in between and was silently skipped) and no
// duplicate (an Event the Snapshot already reported, replayed again).
func TestAPI_Snapshot_ThenStreamFromLastSeq_NoGapNoDuplicate(t *testing.T) {
	b := newBarrierAtCall(1)
	env := newTestEnv(t)
	env.provider.BeforeReturn = b.beforeReturn

	runID := createAndBarrierRun(t, env, b)

	// Snapshot is taken while node_summary's first model call is stalled: some Events
	// (Run started, upstream NodeRuns) have committed, but not all of them.
	lastSeq := snapshotLastSeq(t, env, runID)
	if lastSeq <= 0 {
		t.Fatalf("mid-run snapshot lastSeq = %d, want > 0 (some Events must commit before node_summary's model call stalls)", lastSeq)
	}

	// Release BEFORE opening the stream: the remaining Events (node_summary completing,
	// node_translation, node_output, RUN_COMPLETED) commit while nothing is subscribed
	// yet, so the stream must recover them from afterSeq alone, not from a live wakeup.
	b.Release()

	resp, reader := env.openSSE(t, runID, lastSeq, "")
	defer resp.Body.Close()

	var seqs []int64
	terminal := false
	for {
		frame, err := readSSEFrame(reader)
		if err != nil {
			break
		}
		seqs = append(seqs, frame.id)
		if frame.event == "RUN_COMPLETED" || frame.event == "RUN_FAILED" {
			terminal = true
		}
	}
	if !terminal {
		t.Fatalf("stream opened at afterSeq=%d ended without a terminal Event; seqs received=%v", lastSeq, seqs)
	}
	for _, s := range seqs {
		if s <= lastSeq {
			t.Fatalf("stream opened at afterSeq=%d replayed a pre-snapshot seq=%d (duplicate across the handoff window): %v", lastSeq, s, seqs)
		}
	}

	finalLastSeq := snapshotLastSeq(t, env, runID)

	want := finalLastSeq - lastSeq
	if int64(len(seqs)) != want {
		t.Fatalf("stream from afterSeq=%d delivered %d Events, want %d (lastSeq+1..%d): got seqs=%v", lastSeq, len(seqs), want, finalLastSeq, seqs)
	}
	for i, s := range seqs {
		wantSeq := lastSeq + 1 + int64(i)
		if s != wantSeq {
			t.Fatalf("stream from afterSeq=%d delivered seqs=%v out of order/with a gap: seqs[%d] = %d, want %d", lastSeq, seqs, i, s, wantSeq)
		}
	}
}

// TestAPI_Stream_Reconnect_WithLastEventID_ResumesWithoutDuplicates covers the reconnect
// handoff window: a client that read up through some seq, disconnected, and reconnects
// with Last-Event-ID must resume from seq+1 with no repeats, and Last-Event-ID must win
// over a simultaneously-present afterSeq query parameter.
func TestAPI_Stream_Reconnect_WithLastEventID_ResumesWithoutDuplicates(t *testing.T) {
	b := newBarrierAtCall(1)
	env := newTestEnv(t)
	env.provider.BeforeReturn = b.beforeReturn

	runID := createAndBarrierRun(t, env, b)

	// First connection: read whatever has committed so far (afterSeq=0), then disconnect
	// while the Run is still stalled - a genuine "client went away mid-run" reconnect,
	// not a race with the Run's own completion.
	firstResp, firstReader := env.openSSE(t, runID, 0, "")
	frame, err := readSSEFrame(firstReader)
	if err != nil {
		firstResp.Body.Close()
		t.Fatalf("first connection: readSSEFrame: %v", err)
	}
	lastRead := frame.id
	firstResp.Body.Close()

	b.Release()

	// Reconnect with only Last-Event-ID set (no afterSeq): must resume from lastRead+1.
	secondResp, secondReader := env.openSSE(t, runID, 0, strconv.FormatInt(lastRead, 10))
	defer secondResp.Body.Close()

	var seqs []int64
	terminal := false
	for {
		f, err := readSSEFrame(secondReader)
		if err != nil {
			break
		}
		seqs = append(seqs, f.id)
		if f.event == "RUN_COMPLETED" || f.event == "RUN_FAILED" {
			terminal = true
		}
	}
	if !terminal {
		t.Fatalf("reconnect stream with Last-Event-ID=%d ended without a terminal Event; seqs=%v", lastRead, seqs)
	}
	seen := map[int64]bool{}
	for _, s := range seqs {
		if s <= lastRead {
			t.Fatalf("reconnect stream with Last-Event-ID=%d replayed seq=%d (duplicate of an Event already read on the first connection): %v", lastRead, s, seqs)
		}
		if seen[s] {
			t.Fatalf("reconnect stream with Last-Event-ID=%d delivered seq=%d twice: %v", lastRead, s, seqs)
		}
		seen[s] = true
	}

	// Header-wins-over-afterSeq: open a third connection with a stale
	// afterSeq=0 AND Last-Event-ID=lastRead present together; the first frame delivered
	// must be > lastRead (i.e. the header won), not the Event at seq 1 that afterSeq=0
	// would have replayed.
	env.waitForTerminal(t, runID, 5*time.Second)
	thirdResp, thirdReader := env.openSSERaw(t, runID, "afterSeq=0", strconv.FormatInt(lastRead, 10))
	defer thirdResp.Body.Close()
	thirdFrame, err := readSSEFrame(thirdReader)
	if err != nil {
		t.Fatalf("third connection (header + afterSeq both set): readSSEFrame: %v", err)
	}
	if thirdFrame.id <= lastRead {
		t.Fatalf("third connection: afterSeq=0 and Last-Event-ID=%d both present, first delivered seq=%d; want > %d (Last-Event-ID must win)", lastRead, thirdFrame.id, lastRead)
	}
}

// TestAPI_Stream_NotifierWakesCursor_BeforePollTick covers the fast path (an in-process
// notification wakes the stream's cursor query well before the next poll tick would).
// Its sibling subtest covers the required fallback: the poll tick alone still delivers
// the Event if the notification never arrives. Together they prove neither path is the
// sole source of truth for SSE delivery (CLAUDE.md: "In-process notification only wakes a
// cursor query").
func TestAPI_Stream_NotifierWakesCursor_BeforePollTick(t *testing.T) {
	t.Run("notifier wakes the stream before the poll tick", func(t *testing.T) {
		b := newBarrierAtCall(1)
		env := newTestEnvWithOptions(t, testEnvOptions{PollInterval: 10 * time.Minute})
		env.provider.BeforeReturn = b.beforeReturn

		runID := createAndBarrierRun(t, env, b)
		preSeq := snapshotLastSeq(t, env, runID)

		// Open the stream already caught up to preSeq, so the only way the next frame
		// can arrive is via a fresh commit after release - not backlog replay.
		resp, reader := env.openSSE(t, runID, preSeq, "")
		defer resp.Body.Close()

		b.Release()

		waitForNextSSEFrame(t, reader, 5*time.Second,
			"with PollInterval=10m: the notifier fast path did not wake the stream")
	})

	t.Run("poll tick still delivers the event when the notifier drops it", func(t *testing.T) {
		var dn *dropNotifier
		b := newBarrierAtCall(1)
		env := newTestEnvWithOptions(t, testEnvOptions{
			PollInterval: 20 * time.Millisecond,
			WrapServiceNotifier: func(hub *api.Hub) service.EventNotifier {
				dn = newDropNotifier(hub)
				return dn
			},
		})
		env.provider.BeforeReturn = b.beforeReturn

		runID := createAndBarrierRun(t, env, b)
		dn.SetDropRunID(runID)
		preSeq := snapshotLastSeq(t, env, runID)

		resp, reader := env.openSSE(t, runID, preSeq, "")
		defer resp.Body.Close()

		b.Release()

		waitForNextSSEFrame(t, reader, 5*time.Second,
			"with the notifier dropped for this Run: the PollInterval ticker fallback did not deliver it")
	})
}

// TestAPI_EndToEnd_DocumentProcessing_RunCompletesThroughPoolAndStream is the accepted
// MVP acceptance scenario (document_processing.json through the real work Pool and the
// real Mock Model Provider), consumed entirely through the SSE stream rather than by
// polling GET /runs/{id}: the stream itself must deliver every Event up to and including
// RUN_COMPLETED with nothing missed, and the final Snapshot (taken only after the stream
// has already reported completion) must carry the same output the pipeline produced.
func TestAPI_EndToEnd_DocumentProcessing_RunCompletesThroughPoolAndStream(t *testing.T) {
	env := newTestEnv(t)
	workflowID, version := createFixtureDefinition(t, env)

	_, createBody := env.doJSON(t, http.MethodPost, "/api/runs", map[string]any{
		"workflowId": workflowID, "definitionVersion": version,
		"input": map[string]any{"document": sseTestDocument},
	})
	created := decodeBody[map[string]any](t, createBody)
	runID, _ := created["id"].(string)
	if runID == "" {
		t.Fatalf("POST /api/runs response missing id: %s", createBody)
	}

	resp, reader := env.openSSE(t, runID, 0, "")
	defer resp.Body.Close()

	var seqs []int64
	var runCompletedData string
	for {
		frame, err := readSSEFrame(reader)
		if err != nil {
			t.Fatalf("stream ended before RUN_COMPLETED: %v; seqs so far=%v", err, seqs)
		}
		seqs = append(seqs, frame.id)
		if frame.event == "RUN_FAILED" {
			t.Fatalf("Run reached RUN_FAILED via the stream, want RUN_COMPLETED: %s", frame.data)
		}
		if frame.event == "RUN_COMPLETED" {
			runCompletedData = frame.data
			break
		}
	}
	for i, s := range seqs {
		want := int64(i + 1)
		if s != want {
			t.Fatalf("stream from afterSeq=0 delivered seqs=%v with a gap/duplicate: seqs[%d] = %d, want %d", seqs, i, s, want)
		}
	}
	if runCompletedData == "" {
		t.Fatal("stream ended without ever emitting a RUN_COMPLETED frame")
	}

	// The Snapshot read after the stream itself reported completion must show the same
	// terminal state, not a stale RUNNING view: the stream and the Snapshot both read the
	// same PostgreSQL Event/Run rows (CLAUDE.md: "PostgreSQL Event rows are the authority
	// for SSE"), so there is no ordering window in which one lags the other once the
	// RUN_COMPLETED Event itself has committed.
	finalResp, finalBody := env.doJSON(t, http.MethodGet, "/api/runs/"+runID, nil)
	if finalResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/runs/%s (post-stream snapshot) status = %d, body=%s", runID, finalResp.StatusCode, finalBody)
	}
	finalSnap := decodeBody[map[string]any](t, finalBody)
	finalRun, _ := finalSnap["run"].(map[string]any)
	if status, _ := finalRun["status"].(string); status != "COMPLETED" {
		t.Fatalf("post-stream snapshot Run.status = %q, want %q: %v", status, "COMPLETED", finalRun)
	}
	if _, ok := finalRun["output"]; !ok {
		t.Fatalf("post-stream snapshot Run missing output: %v", finalRun)
	}
}

// TestAPI_Stream_EmitsKeepaliveOnIdlePoll covers stream.go's idle-poll-tick branch (item 9
// of the contract review): with the Run stalled at a barrier (RUNNING, nothing new to
// commit) and a short PollInterval, the connection must still send a ": keepalive\n\n"
// comment frame on every tick it finds nothing new, so a proxy or load balancer between
// the client and this connection never treats it as dead.
func TestAPI_Stream_EmitsKeepaliveOnIdlePoll(t *testing.T) {
	b := newBarrierAtCall(1)
	env := newTestEnvWithOptions(t, testEnvOptions{PollInterval: 30 * time.Millisecond})
	env.provider.BeforeReturn = b.beforeReturn

	runID := createAndBarrierRun(t, env, b)
	preSeq := snapshotLastSeq(t, env, runID)

	// Open already caught up to preSeq: with the barrier held, no further Event can
	// commit, so any bytes the connection sends from here on must be keepalive frames,
	// not real backlog.
	resp, reader := env.openSSE(t, runID, preSeq, "")
	defer resp.Body.Close()
	defer b.Release()

	found := make(chan struct{})
	go func() {
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			if strings.TrimRight(line, "\r\n") == ": keepalive" {
				close(found)
				return
			}
		}
	}()

	select {
	case <-found:
	case <-time.After(2 * time.Second):
		t.Fatal("no \": keepalive\" comment frame observed within 2s of an idle, non-terminal stream with PollInterval=30ms")
	}
}

// waitForNextSSEFrame reads one more SSE frame from r, failing the test if none arrives
// within timeout. The read runs on its own goroutine since bufio.Reader has no deadline of
// its own; the test's context.Context timeout bounds how long the test waits, never a
// sleep.
func waitForNextSSEFrame(t *testing.T, r *bufio.Reader, timeout time.Duration, failMsg string) sseFrame {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	frameCh := make(chan sseFrame, 1)
	errCh := make(chan error, 1)
	go func() {
		frame, err := readSSEFrame(r)
		if err != nil {
			errCh <- err
			return
		}
		frameCh <- frame
	}()

	select {
	case frame := <-frameCh:
		return frame
	case err := <-errCh:
		t.Fatalf("stream ended before delivering the expected Event, %s: %v", failMsg, err)
	case <-ctx.Done():
		t.Fatalf("no Event arrived within %s, %s", timeout, failMsg)
	}
	return sseFrame{}
}
