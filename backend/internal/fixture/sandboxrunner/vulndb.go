package sandboxrunner

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// The vulnerability database snapshot is the fixed set of advisories every verification is
// scanned against, so a verdict does not change when the public database is updated. The
// same files are copied into the verification image for govulncheck, which reads them as
// GOVULNDB=file:///vulndb, and are read here to compare the full dependency list of a
// patched module against the affected version ranges. Its digest is reported with every
// result so a verdict names the snapshot it was reached with.

//go:embed testdata/vulndb
var vulndbFS embed.FS

// vulndbRoot is the embedded snapshot's directory. Its layout is the one govulncheck
// expects: index/db.json, index/modules.json, index/vulns.json and ID/<id>.json.
const vulndbRoot = "testdata/vulndb"

// vulnSnapshot is the parsed snapshot: every advisory with the module version ranges it
// affects, and the digest of the files it was read from.
type vulnSnapshot struct {
	entries []vulnEntry
	digest  string
}

// vulnEntry is one advisory as the dependency comparison needs it.
type vulnEntry struct {
	ID       string
	Affected []affectedModule
}

// affectedModule is one module path with the version ranges an advisory affects. A range
// with no fixed version affects every version from its introduction on.
type affectedModule struct {
	Path   string
	Ranges []versionRange
}

// versionRange is a half-open semantic version interval [Introduced, Fixed). Introduced
// "0" means every version; Fixed "" means no fix is published.
type versionRange struct {
	Introduced string
	Fixed      string
}

// moduleVersion is one module of a build list, as `go list -m -json all` reports it.
type moduleVersion struct {
	Path     string `json:"Path"`
	Version  string `json:"Version"`
	Main     bool   `json:"Main"`
	Indirect bool   `json:"Indirect"`
}

// vulnerableDependency names one build-list module whose version an advisory affects.
type vulnerableDependency struct {
	Module   string
	Version  string
	Advisory string
	Indirect bool
}

// String renders the dependency as the result reports it: module@version (advisory).
func (d vulnerableDependency) String() string {
	suffix := ""
	if d.Indirect {
		suffix = ", indirect"
	}
	return fmt.Sprintf("%s@%s (%s%s)", d.Module, d.Version, d.Advisory, suffix)
}

// osvEntry is the part of an OSV advisory the snapshot reads.
type osvEntry struct {
	ID       string `json:"id"`
	Affected []struct {
		Package struct {
			Name string `json:"name"`
		} `json:"package"`
		Ranges []struct {
			Type   string `json:"type"`
			Events []struct {
				Introduced string `json:"introduced"`
				Fixed      string `json:"fixed"`
			} `json:"events"`
		} `json:"ranges"`
	} `json:"affected"`
}

// loadVulnSnapshot parses the embedded snapshot. It fails rather than scanning against an
// empty set when the snapshot is malformed: a verification that cannot read its database
// must not report a clean dependency list.
func loadVulnSnapshot() (*vulnSnapshot, error) {
	return loadVulnSnapshotFS(vulndbFS, vulndbRoot)
}

func loadVulnSnapshotFS(fsys fs.FS, root string) (*vulnSnapshot, error) {
	snapshot := &vulnSnapshot{}
	digest := sha256.New()
	var paths []string
	err := fs.WalkDir(fsys, root, func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		paths = append(paths, name)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("sandboxrunner: read vulnerability snapshot: %w", err)
	}
	sort.Strings(paths)
	for _, name := range paths {
		content, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, fmt.Errorf("sandboxrunner: read vulnerability snapshot: %w", err)
		}
		// The digest covers every file name and content, in sorted order, so the same
		// snapshot always yields the same digest wherever it is read from.
		rel := strings.TrimPrefix(name, root+"/")
		digest.Write([]byte(rel))
		digest.Write([]byte{0})
		digest.Write(content)
		digest.Write([]byte{0})
		if !strings.HasPrefix(rel, "ID/") || !strings.HasSuffix(rel, ".json") {
			continue
		}
		entry, err := parseOSVEntry(content)
		if err != nil {
			return nil, fmt.Errorf("sandboxrunner: vulnerability snapshot %s: %w", rel, err)
		}
		snapshot.entries = append(snapshot.entries, entry)
	}
	if len(snapshot.entries) == 0 {
		return nil, fmt.Errorf("sandboxrunner: vulnerability snapshot %s holds no advisory", root)
	}
	snapshot.digest = "sha256:" + hex.EncodeToString(digest.Sum(nil))
	return snapshot, nil
}

func parseOSVEntry(content []byte) (vulnEntry, error) {
	var osv osvEntry
	if err := json.Unmarshal(content, &osv); err != nil {
		return vulnEntry{}, err
	}
	if osv.ID == "" || len(osv.Affected) == 0 {
		return vulnEntry{}, fmt.Errorf("advisory has no id or no affected module")
	}
	entry := vulnEntry{ID: osv.ID}
	for _, affected := range osv.Affected {
		module := affectedModule{Path: affected.Package.Name}
		if module.Path == "" {
			return vulnEntry{}, fmt.Errorf("advisory %s affects a module without a path", osv.ID)
		}
		for _, r := range affected.Ranges {
			if r.Type != "SEMVER" {
				continue
			}
			var current *versionRange
			for _, event := range r.Events {
				switch {
				case event.Introduced != "":
					module.Ranges = append(module.Ranges, versionRange{Introduced: event.Introduced})
					current = &module.Ranges[len(module.Ranges)-1]
				case event.Fixed != "" && current != nil:
					current.Fixed = event.Fixed
					current = nil
				}
			}
		}
		if len(module.Ranges) == 0 {
			return vulnEntry{}, fmt.Errorf("advisory %s has no semantic version range for %s", osv.ID, module.Path)
		}
		entry.Affected = append(entry.Affected, module)
	}
	return entry, nil
}

// vulnerableDependencies lists every module of the build list, the main module excepted,
// whose version falls into an affected range of any advisory in the snapshot, in build-list
// order. Every module counts, direct or indirect: a vulnerable version anywhere in the list
// is still built.
func (s *vulnSnapshot) vulnerableDependencies(modules []moduleVersion) []vulnerableDependency {
	var out []vulnerableDependency
	for _, module := range modules {
		if module.Main || module.Version == "" {
			continue
		}
		for _, entry := range s.entries {
			for _, affected := range entry.Affected {
				if affected.Path != module.Path {
					continue
				}
				if versionAffected(module.Version, affected.Ranges) {
					out = append(out, vulnerableDependency{Module: module.Path, Version: module.Version, Advisory: entry.ID, Indirect: module.Indirect})
				}
			}
		}
	}
	return out
}

// versionAffected reports whether version lies in any of the ranges.
func versionAffected(version string, ranges []versionRange) bool {
	for _, r := range ranges {
		if compareSemver(version, r.Introduced) < 0 {
			continue
		}
		if r.Fixed == "" || compareSemver(version, r.Fixed) < 0 {
			return true
		}
	}
	return false
}

// parseModuleList reads the JSON stream `go list -m -json all` writes.
func parseModuleList(output []byte) ([]moduleVersion, error) {
	decoder := json.NewDecoder(strings.NewReader(string(output)))
	var modules []moduleVersion
	for decoder.More() {
		var module moduleVersion
		if err := decoder.Decode(&module); err != nil {
			return nil, fmt.Errorf("module list is not a stream of JSON objects: %w", err)
		}
		if module.Path == "" {
			return nil, fmt.Errorf("module list entry has no path")
		}
		modules = append(modules, module)
	}
	return modules, nil
}

// compareSemver orders two semantic versions by major, minor, patch and pre-release, the
// way Go module versions compare. A leading "v" and build metadata, such as the
// "+incompatible" suffix, are ignored; a version with a pre-release orders before the same
// version without one. "0" is the lowest version.
func compareSemver(a, b string) int {
	ma, pa := splitSemver(a)
	mb, pb := splitSemver(b)
	for i := range 3 {
		if ma[i] != mb[i] {
			if ma[i] < mb[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case pa == pb:
		return 0
	case pa == "":
		return 1
	case pb == "":
		return -1
	}
	return comparePrerelease(pa, pb)
}

// splitSemver returns the three numeric components and the pre-release part of a version.
// Missing components are zero; an unparsable component is also zero, which keeps the
// comparison total.
func splitSemver(version string) ([3]int, string) {
	version = strings.TrimPrefix(version, "v")
	if plus := strings.IndexByte(version, '+'); plus >= 0 {
		version = version[:plus]
	}
	var prerelease string
	if dash := strings.IndexByte(version, '-'); dash >= 0 {
		version, prerelease = version[:dash], version[dash+1:]
	}
	var parts [3]int
	for i, text := range strings.SplitN(version, ".", 3) {
		if i < 3 {
			parts[i], _ = strconv.Atoi(text)
		}
	}
	return parts, prerelease
}

func comparePrerelease(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := range min(len(as), len(bs)) {
		na, errA := strconv.Atoi(as[i])
		nb, errB := strconv.Atoi(bs[i])
		switch {
		case errA == nil && errB == nil:
			if na != nb {
				return slices.Compare([]int{na}, []int{nb})
			}
		case errA == nil:
			return -1
		case errB == nil:
			return 1
		default:
			if c := strings.Compare(as[i], bs[i]); c != 0 {
				return c
			}
		}
	}
	return slices.Compare([]int{len(as)}, []int{len(bs)})
}
