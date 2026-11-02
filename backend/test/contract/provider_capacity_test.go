//go:build integration

package contract

import (
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
)

// waitForImageQueued polls until the Run's image NodeRun is READY and marked as waiting
// for a Provider slot, and returns that NodeRun as the Snapshot serves it.
func (e *testEnv) waitForImageQueued(t *testing.T, runID string, timeout time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		nr := e.nodeRunOf(t, runID, servingImageNode)
		if nr != nil && nr["status"] == "READY" && nr["capacityDeferredAt"] != nil {
			return nr
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("image NodeRun of run %s was not queued for a Provider slot within %s", runID, timeout)
	return nil
}

// TestAPI_ProviderCapacity_QueuedNodeRun_SnapshotAndDetailProjectTheWait: with one slot on
// the image Provider and two users, the second image task stays READY and queued. The
// Snapshot carries when and for which Provider it queued, the detail carries its live
// position, and an admitted or finished NodeRun carries no wait at all.
func TestAPI_ProviderCapacity_QueuedNodeRun_SnapshotAndDetailProjectTheWait(t *testing.T) {
	fx := newServingFixture(t, false)
	fx.capacity = map[string]int{mockmodel.ImageModelProviderID: 1}
	env := fx.startBackend(t, nil, nil)
	workflowID, version := saveServingDefinition(t, env, servingDefinition(t))
	photo := servingPhoto(t, env)
	base := fx.lastSeq(t)

	first := env.servingUsersSubmit(t, workflowID, version, photo, 1)
	env.waitAllImagesWaiting(t, first)
	second := env.servingUsersSubmit(t, workflowID, version, photo, 1)
	queued := env.waitForImageQueued(t, second[0], servingWait)

	if got, _ := queued["capacityProviderId"].(string); got != mockmodel.ImageModelProviderID {
		t.Fatalf("queued NodeRun capacityProviderId = %v, want %q", queued["capacityProviderId"], mockmodel.ImageModelProviderID)
	}
	if _, isString := queued["capacityDeferredAt"].(string); !isString {
		t.Fatalf("queued NodeRun capacityDeferredAt = %v, want a timestamp", queued["capacityDeferredAt"])
	}
	admitted := env.nodeRunOf(t, first[0], servingImageNode)
	for _, key := range []string{"capacityDeferredAt", "capacityProviderId"} {
		if value, present := admitted[key]; !present || value != nil {
			t.Errorf("admitted NodeRun %s = %v (present %v), want null", key, value, present)
		}
	}

	queuedID, _ := queued["id"].(string)
	detail, body := env.nodeRunDetail(t, second[0], queuedID)
	wait, _ := detail["capacityWait"].(map[string]any)
	if wait == nil {
		t.Fatalf("queued NodeRun detail capacityWait = %v, want an object: %s", detail["capacityWait"], body)
	}
	if wait["providerId"] != mockmodel.ImageModelProviderID || wait["limit"] != float64(1) || wait["held"] != float64(1) || wait["position"] != float64(1) {
		t.Fatalf("capacityWait = %v, want provider %q limit 1 held 1 position 1", wait, mockmodel.ImageModelProviderID)
	}
	admittedID, _ := admitted["id"].(string)
	admittedDetail, body := env.nodeRunDetail(t, first[0], admittedID)
	if value, present := admittedDetail["capacityWait"]; !present || value != nil {
		t.Fatalf("admitted NodeRun capacityWait = %v (present %v), want null: %s", value, present, body)
	}
	events := env.eventsForNodeRun(t, second[0], queuedID)
	var waiting int
	for _, ev := range events {
		if ev["type"] == "NODE_WAITING_CAPACITY" {
			waiting++
			payload, _ := ev["payload"].(map[string]any)
			if payload["providerId"] != mockmodel.ImageModelProviderID || payload["limit"] != float64(1) || payload["position"] != float64(1) {
				t.Errorf("NODE_WAITING_CAPACITY payload = %v, want provider, limit 1, position 1", payload)
			}
		}
	}
	if waiting != 1 {
		t.Fatalf("queued NodeRun has %d NODE_WAITING_CAPACITY Events, want 1", waiting)
	}

	// Releasing the Provider's answers frees the slot; the queued Run takes it and both
	// finish. The queued-since fact stays recorded, the live wait is gone.
	fx.callbacks.releaseAll()
	runIDs := append(first, second...)
	env.waitAllCompleted(t, runIDs)
	finished, body := env.nodeRunDetail(t, second[0], queuedID)
	if value, present := finished["capacityWait"]; !present || value != nil {
		t.Fatalf("finished NodeRun capacityWait = %v (present %v), want null: %s", value, present, body)
	}
	finishedNR, _ := finished["nodeRun"].(map[string]any)
	if _, isString := finishedNR["capacityDeferredAt"].(string); !isString {
		t.Fatalf("finished NodeRun capacityDeferredAt = %v, want the recorded timestamp kept", finishedNR["capacityDeferredAt"])
	}
	readServingLedger(t, env, fx, runIDs, base).assertOneToOne(t)
}
