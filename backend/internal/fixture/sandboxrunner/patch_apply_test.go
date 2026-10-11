package sandboxrunner

import (
	"errors"
	"strings"
	"testing"
)

// touchPatch changes only a comment in the fixture, so the fixture's test still passes.
const touchPatch = `--- a/greet.go
+++ b/greet.go
@@ -5,4 +5,4 @@
-// Greeting returns the greeting for name.
+// Greeting returns the friendly greeting for name.
 func Greeting(name string) string {
 	return "Hello, " + name + "!"
 }
`

// breakingPatch changes the greeting the fixture's test expects.
const breakingPatch = `--- a/greet.go
+++ b/greet.go
@@ -6,3 +6,3 @@
 func Greeting(name string) string {
-	return "Hello, " + name + "!"
+	return "Hi, " + name + "!"
 }
`

func patchedFixture(t *testing.T, patch string) (map[string][]byte, error) {
	t.Helper()
	files, err := fixtureFiles()
	if err != nil {
		t.Fatalf("fixture files: %v", err)
	}
	return files, applyPatch(files, patch)
}

func TestApplyPatch_ModifiesTheNamedLines(t *testing.T) {
	original, err := fixtureFiles()
	if err != nil {
		t.Fatalf("fixture files: %v", err)
	}
	files, err := patchedFixture(t, breakingPatch)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	want := strings.Replace(string(original["greet.go"]), `"Hello, "`, `"Hi, "`, 1)
	if string(files["greet.go"]) != want {
		t.Fatalf("greet.go = %q, want %q", files["greet.go"], want)
	}
}

func TestApplyPatch_CreatesAndDeletesFiles(t *testing.T) {
	files := map[string][]byte{"old.go": []byte("package old\n\nvar x = 1\n")}
	patch := `diff --git a/sub/extra.go b/sub/extra.go
--- /dev/null
+++ b/sub/extra.go
@@ -0,0 +1,2 @@
+package sub
+// Extra is new.
--- a/old.go
+++ /dev/null
@@ -1,3 +0,0 @@
-package old
-
-var x = 1
`
	if err := applyPatch(files, patch); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, ok := files["old.go"]; ok {
		t.Fatal("old.go still present after deletion")
	}
	if string(files["sub/extra.go"]) != "package sub\n// Extra is new.\n" {
		t.Fatalf("sub/extra.go = %q", files["sub/extra.go"])
	}
}

func TestApplyPatch_RejectsWithoutChangingTheFixture(t *testing.T) {
	cases := map[string]string{
		"context mismatch": strings.Replace(touchPatch, "returns the greeting", "returns a greeting", 1),
		"wrong line":       strings.Replace(touchPatch, "@@ -5,4 +5,4 @@", "@@ -4,4 +4,4 @@", 1),
		"missing file":     strings.ReplaceAll(touchPatch, "greet.go", "absent.go"),
		"parent escape":    strings.Replace(touchPatch, "+++ b/greet.go", "+++ b/../greet.go", 1),
		"absolute path":    strings.Replace(touchPatch, "+++ b/greet.go", "+++ /etc/passwd", 1),
		"hidden file":      strings.Replace(touchPatch, "+++ b/greet.go", "+++ b/.git/config", 1),
		"short hunk":       strings.TrimSuffix(touchPatch, " }\n"),
		"long hunk":        strings.Replace(touchPatch, "@@ -5,4 +5,4 @@", "@@ -5,2 +5,2 @@", 1),
		"bad header":       strings.Replace(touchPatch, "@@ -5,4 +5,4 @@", "@@ five @@", 1),
		"no diff":          "just some text\n",
		"empty":            "",
		"creates existing": "--- /dev/null\n+++ b/greet.go\n@@ -0,0 +1 @@\n+package greet\n",
	}
	for name, patch := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := patchedFixture(t, patch); !errors.Is(err, errPatchRejected) {
				t.Fatalf("apply error = %v, want errPatchRejected", err)
			}
		})
	}
}
