package sandboxrunner

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestCompareSemver_OrdersVersionsLikeGoModules(t *testing.T) {
	ordered := []string{"0", "v0.9.0", "v1.0.0-rc.1", "v1.0.0", "v3.2.2+incompatible", "v4.5.1", "v4.5.2", "v5.0.0-rc.1", "v5.0.0", "v5.2.2", "v5.3.1"}
	for i := 1; i < len(ordered); i++ {
		if compareSemver(ordered[i-1], ordered[i]) >= 0 || compareSemver(ordered[i], ordered[i-1]) <= 0 {
			t.Fatalf("%s should order before %s", ordered[i-1], ordered[i])
		}
	}
	if compareSemver("v3.2.2+incompatible", "3.2.2") != 0 {
		t.Fatal("build metadata and the v prefix must not affect the order")
	}
}

func TestVersionAffected_HalfOpenRanges(t *testing.T) {
	ranges := []versionRange{{Introduced: "0", Fixed: "4.5.2"}, {Introduced: "5.0.0-rc.1", Fixed: "5.2.2"}}
	for version, want := range map[string]bool{
		"v3.2.2+incompatible": true, "v4.5.1": true, "v4.5.2": false, "v4.9.0": false,
		"v5.0.0-rc.1": true, "v5.2.1": true, "v5.2.2": false, "v5.3.1": false,
	} {
		if got := versionAffected(version, ranges); got != want {
			t.Errorf("versionAffected(%s) = %t, want %t", version, got, want)
		}
	}
	if !versionAffected("v99.0.0", []versionRange{{Introduced: "0"}}) {
		t.Fatal("a range without a fixed version must affect every later version")
	}
}

func TestLoadVulnSnapshot_EmbeddedSnapshot_HoldsTheAdvisory(t *testing.T) {
	snapshot, err := loadVulnSnapshot()
	if err != nil {
		t.Fatalf("loadVulnSnapshot: %v", err)
	}
	if !strings.HasPrefix(snapshot.digest, "sha256:") || len(snapshot.digest) != len("sha256:")+64 {
		t.Fatalf("digest = %q, want sha256:<64 hex>", snapshot.digest)
	}
	again, _ := loadVulnSnapshot()
	if again.digest != snapshot.digest {
		t.Fatal("the same snapshot yields different digests")
	}
	modules := []moduleVersion{
		{Path: "example.com/tokenservice", Main: true},
		{Path: "github.com/golang-jwt/jwt", Version: "v3.2.2+incompatible"},
		{Path: "github.com/golang-jwt/jwt/v4", Version: "v4.5.1", Indirect: true},
		{Path: "github.com/golang-jwt/jwt/v5", Version: "v5.3.1"},
	}
	got := snapshot.vulnerableDependencies(modules)
	if len(got) != 2 || got[0].String() != "github.com/golang-jwt/jwt@v3.2.2+incompatible (GO-2025-3553)" || got[1].String() != "github.com/golang-jwt/jwt/v4@v4.5.1 (GO-2025-3553, indirect)" {
		t.Fatalf("vulnerable dependencies = %v", got)
	}
}

func TestLoadVulnSnapshotFS_NoAdvisory_Fails(t *testing.T) {
	fsys := fstest.MapFS{"db/index/db.json": {Data: []byte(`{"modified":"2025-01-01T00:00:00Z"}`)}}
	if _, err := loadVulnSnapshotFS(fsys, "db"); err == nil {
		t.Fatal("a snapshot without an advisory loaded")
	}
	fsys["db/ID/GO-0000-0001.json"] = &fstest.MapFile{Data: []byte(`{"id":"GO-0000-0001","affected":[{"package":{"name":"example.com/m"},"ranges":[{"type":"GIT","events":[]}]}]}`)}
	if _, err := loadVulnSnapshotFS(fsys, "db"); err == nil || !strings.Contains(err.Error(), "no semantic version range") {
		t.Fatalf("err = %v, want a missing semantic version range", err)
	}
}

func TestParseModuleList_ReadsTheJSONStream(t *testing.T) {
	modules, err := parseModuleList([]byte(`{"Path":"example.com/m","Main":true,"Dir":"/w"}
{"Path":"github.com/golang-jwt/jwt","Version":"v3.2.2+incompatible","Indirect":true}
`))
	if err != nil || len(modules) != 2 || !modules[0].Main || modules[1].Version != "v3.2.2+incompatible" || !modules[1].Indirect {
		t.Fatalf("modules = %+v, err = %v", modules, err)
	}
	if _, err := parseModuleList([]byte(`{"Path":"a"} garbage`)); err == nil {
		t.Fatal("garbage after the stream parsed")
	}
}
