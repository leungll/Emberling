package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

func TestWorkspacePatchStale_ComparesTheNamedDigestWithTheCurrentPatch(t *testing.T) {
	view := &registry.WorkspaceView{PatchDigest: domain.WorkspacePatchDigest("current"), PatchSeq: 2}
	bound := domain.ToolMetadata{Name: "verify_patch", UsesWorkspace: true, WorkspacePatchDigestPath: "/patchDigest"}
	current, _ := json.Marshal(map[string]string{"patchDigest": view.PatchDigest})
	if named, stale := workspacePatchStale(bound, current, view); stale || named != view.PatchDigest {
		t.Fatalf("current digest: named %q stale %t, want not stale", named, stale)
	}
	old := domain.WorkspacePatchDigest("older")
	older, _ := json.Marshal(map[string]string{"patchDigest": old})
	named, stale := workspacePatchStale(bound, older, view)
	if !stale || named != old {
		t.Fatalf("older digest: named %q stale %t, want stale", named, stale)
	}
	message := stalePatchMessage(named, view)
	if !strings.Contains(message, old) || !strings.Contains(message, view.PatchDigest) || !strings.Contains(message, "patch 2") {
		t.Fatalf("message %q should name both digests and the current patch number", message)
	}
	if _, stale := workspacePatchStale(bound, json.RawMessage(`{"patchDigest":7}`), view); !stale {
		t.Fatal("a digest that is not a string must count as stale")
	}
	unbound := domain.ToolMetadata{Name: "read_file", UsesWorkspace: true}
	if _, stale := workspacePatchStale(unbound, older, view); stale {
		t.Fatal("a Tool without a declared digest argument is never stale")
	}
}
