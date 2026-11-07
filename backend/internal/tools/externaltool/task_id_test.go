package externaltool_test

import (
	"regexp"
	"testing"

	"github.com/leungll/Emberling/backend/internal/tools/externaltool"
)

func TestTaskID_StablePerAttemptDistinctAcrossAttemptsAndActions(t *testing.T) {
	first := externaltool.TaskID("act_a", 1)
	if first != externaltool.TaskID("act_a", 1) {
		t.Fatalf("the same Attempt produced two ids: %s and %s", first, externaltool.TaskID("act_a", 1))
	}
	if !regexp.MustCompile(`^ext_[0-9a-f]{32}$`).MatchString(first) {
		t.Fatalf("id = %q, want the ext_ prefix and 32 hex characters", first)
	}
	seen := map[string]string{first: "act_a/1"}
	for _, tc := range []struct {
		actionID string
		attempt  int
	}{{"act_a", 2}, {"act_b", 1}, {"act_a1", 1}, {"act_a", 11}, {"act_", 1}} {
		id := externaltool.TaskID(tc.actionID, tc.attempt)
		if other, taken := seen[id]; taken {
			t.Errorf("%s/%d collides with %s on %s", tc.actionID, tc.attempt, other, id)
		}
		seen[id] = tc.actionID
	}
}
