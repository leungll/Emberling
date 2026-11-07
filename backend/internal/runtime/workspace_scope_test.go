package runtime

import (
	"errors"
	"reflect"
	"testing"
)

var scopeAllowed = []string{"go.mod", "go.sum", "internal/auth"}

func assertScopeRefused(t *testing.T, filePath string, want WriteScopeReason) {
	t.Helper()
	normalised, err := CheckWriteScope(scopeAllowed, filePath)
	if err == nil {
		t.Fatalf("CheckWriteScope(%q) = %q, nil; want refusal %s", filePath, normalised, want)
	}
	var scope *WriteScopeError
	if !errors.As(err, &scope) || scope.Reason != want {
		t.Fatalf("CheckWriteScope(%q) = %v, want a WriteScopeError with reason %s", filePath, err, want)
	}
	if !IsWriteScopeError(err) {
		t.Errorf("IsWriteScopeError(%v) = false", err)
	}
}

func TestCheckWriteScope_ParentSegment_Refused(t *testing.T) {
	for _, p := range []string{"internal/auth/../../main.go", "../go.mod", "internal/./auth/x.go", `internal\auth\x.go`} {
		assertScopeRefused(t, p, WriteScopeParentSegment)
	}
}

func TestCheckWriteScope_AbsolutePath_Refused(t *testing.T) {
	for _, p := range []string{"/etc/passwd", "/go.mod", "/internal/auth/x.go"} {
		assertScopeRefused(t, p, WriteScopeAbsolute)
	}
}

func TestCheckWriteScope_OutsideAllowedPaths_Refused(t *testing.T) {
	for _, p := range []string{"main.go", "internal/authz/x.go", "internal/auth_test.go", "internal", "go.mod.bak", "vendor/go.mod"} {
		assertScopeRefused(t, p, WriteScopeOutsideAllowed)
	}
	assertScopeRefused(t, "", WriteScopeEmpty)
	// Nothing is allowed when the frozen list is empty.
	if _, err := CheckWriteScope(nil, "go.mod"); err == nil {
		t.Errorf("an empty allowed list permitted a write")
	}
}

func TestCheckWriteScope_InsideAllowedPaths_ReturnsNormalisedPath(t *testing.T) {
	cases := map[string]string{
		"go.mod":                      "go.mod",
		"internal/auth/token.go":      "internal/auth/token.go",
		"internal/auth":               "internal/auth",
		"internal/auth/":              "internal/auth",
		"internal//auth/deep/file.go": "internal/auth/deep/file.go",
	}
	for in, want := range cases {
		got, err := CheckWriteScope(scopeAllowed, in)
		if err != nil || got != want {
			t.Errorf("CheckWriteScope(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestNormaliseAllowedPaths_KeepsOrderAndRejectsRoot(t *testing.T) {
	got, err := NormaliseAllowedPaths([]string{"internal/", "go.mod", "pkg//sub/"})
	if err != nil {
		t.Fatalf("NormaliseAllowedPaths: %v", err)
	}
	if want := []string{"internal", "go.mod", "pkg/sub"}; !reflect.DeepEqual(got, want) {
		t.Errorf("normalised = %v, want %v", got, want)
	}
	for _, bad := range [][]string{{"."}, {""}, {"/abs"}, {"a/../b"}} {
		if _, err := NormaliseAllowedPaths(bad); err == nil {
			t.Errorf("NormaliseAllowedPaths(%v) accepted an invalid prefix", bad)
		}
	}
}

func TestResolveWorkspaceWritePath_ReadsDeclaredArgument(t *testing.T) {
	got, err := ResolveWorkspaceWritePath([]byte(`{"path":"internal/auth/token.go","content":"x"}`), "/path")
	if err != nil || got != "internal/auth/token.go" {
		t.Fatalf("resolve = %q, %v; want the path argument", got, err)
	}
	for name, args := range map[string]string{
		"missing":    `{"content":"x"}`,
		"not string": `{"path":3}`,
		"empty":      `{"path":""}`,
		"malformed":  `{`,
	} {
		if _, err := ResolveWorkspaceWritePath([]byte(args), "/path"); !IsWriteScopeError(err) {
			t.Errorf("%s: resolve = %v, want a write scope refusal", name, err)
		}
	}
}
