package sandboxrunner

import (
	"testing"

	"github.com/leungll/Emberling/backend/internal/fixture/mockax"
)

// The AX stand-in satisfies both narrow interfaces by method signature alone.
var (
	_ AXTaskControl  = (*mockax.Fake)(nil)
	_ AXResultReader = (*mockax.Fake)(nil)
)

func TestAXTaskStates_MatchTheStandInsValues(t *testing.T) {
	pairs := [][2]string{
		{AXTaskPending, mockax.TaskPending},
		{AXTaskReady, mockax.TaskReady},
		{AXTaskFailed, mockax.TaskFailed},
		{AXTaskSuspended, mockax.TaskSuspended},
		{AXTaskMissing, mockax.TaskMissing},
	}
	for _, pair := range pairs {
		if pair[0] != pair[1] {
			t.Fatalf("runner state %q differs from the stand-in's %q", pair[0], pair[1])
		}
	}
}
