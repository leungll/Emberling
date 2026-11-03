package sandboxrunner

import (
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// The runner does not share code with the Runtime, so the rule it reports a patch digest
// by is restated here and proven equal to the Runtime's: a result binds to the digest the
// Runtime recorded for the workspace patch, or the fact it produces binds to nothing.
func TestWorkspacePatchDigest_MatchesTheRuntimeRule(t *testing.T) {
	for _, patch := range []string{"", "a\n", "a\r\nb\n", "lone\rcr\n", testPatch} {
		if got, want := WorkspacePatchDigest(patch), domain.WorkspacePatchDigest(patch); got != want {
			t.Fatalf("WorkspacePatchDigest(%q) = %s, Runtime computes %s", patch, got, want)
		}
	}
}
