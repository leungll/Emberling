package sandboxrunner

import (
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
)

// maxPatchedFiles bounds how many files one patch may touch.
const maxPatchedFiles = 64

// errPatchRejected reports a patch that is malformed or does not apply exactly.
var errPatchRejected = errors.New("patch does not apply")

// applyPatch applies a unified diff with one leading path component (a/ and b/) to
// files, in place. It is strict: every hunk must apply at exactly the line it names, with
// matching context, so a patch either applies as written or is rejected; there is no fuzz
// and no offset search. A path must stay inside the fixture: absolute paths, ".." and
// hidden components are rejected. /dev/null as the old side creates a file and as the new
// side deletes one.
func applyPatch(files map[string][]byte, patch string) error {
	lines := strings.SplitAfter(patch, "\n")
	touched := 0
	for i := 0; i < len(lines); {
		if !strings.HasPrefix(lines[i], "--- ") {
			// Text before or between file diffs, such as a "diff --git" line, is ignored.
			i++
			continue
		}
		if i+1 >= len(lines) || !strings.HasPrefix(lines[i+1], "+++ ") {
			return fmt.Errorf("%w: file header without +++ line", errPatchRejected)
		}
		oldPath, err := patchPath(lines[i][4:])
		if err != nil {
			return err
		}
		newPath, err := patchPath(lines[i+1][4:])
		if err != nil {
			return err
		}
		touched++
		if touched > maxPatchedFiles {
			return fmt.Errorf("%w: more than %d files", errPatchRejected, maxPatchedFiles)
		}
		i += 2

		var original []string
		switch {
		case oldPath == "" && newPath == "":
			return fmt.Errorf("%w: both sides are /dev/null", errPatchRejected)
		case oldPath == "":
			if _, exists := files[newPath]; exists {
				return fmt.Errorf("%w: creates an existing file", errPatchRejected)
			}
		default:
			content, exists := files[oldPath]
			if !exists {
				return fmt.Errorf("%w: modifies a file the base commit does not have", errPatchRejected)
			}
			original = strings.SplitAfter(string(content), "\n")
			if original[len(original)-1] == "" {
				original = original[:len(original)-1]
			}
		}

		var result []string
		next := 0 // index into original of the first line not yet copied
		for i < len(lines) && strings.HasPrefix(lines[i], "@@ ") {
			oldStart, oldCount, newCount, err := parseHunkHeader(lines[i])
			if err != nil {
				return err
			}
			i++
			start := oldStart - 1
			if oldCount == 0 {
				start = oldStart // an empty old range names the line after which to insert
			}
			if start < next || start > len(original) {
				return fmt.Errorf("%w: hunk out of order or out of range", errPatchRejected)
			}
			result = append(result, original[next:start]...)
			next = start
			seenOld, seenNew := 0, 0
			for seenOld < oldCount || seenNew < newCount {
				if i >= len(lines) || lines[i] == "" {
					return fmt.Errorf("%w: hunk ends early", errPatchRejected)
				}
				line := lines[i]
				i++
				body := line[1:]
				switch line[0] {
				case ' ', '-':
					if next >= len(original) || original[next] != body {
						return fmt.Errorf("%w: context does not match", errPatchRejected)
					}
					next++
					seenOld++
					if line[0] == ' ' {
						result = append(result, body)
						seenNew++
					}
				case '+':
					result = append(result, body)
					seenNew++
				case '\n':
					// A blank line in a hand-edited patch stands for an empty context line.
					if next >= len(original) || original[next] != "\n" {
						return fmt.Errorf("%w: context does not match", errPatchRejected)
					}
					result = append(result, "\n")
					next++
					seenOld++
					seenNew++
				default:
					return fmt.Errorf("%w: unexpected hunk line", errPatchRejected)
				}
				if seenOld > oldCount || seenNew > newCount {
					return fmt.Errorf("%w: hunk longer than its header", errPatchRejected)
				}
				if i < len(lines) && strings.HasPrefix(lines[i], `\ No newline at end of file`) {
					// The preceding line has no trailing newline on the side it belongs to.
					if line[0] != '-' && len(result) > 0 {
						result[len(result)-1] = strings.TrimSuffix(result[len(result)-1], "\n")
					}
					i++
				}
			}
		}
		if i < len(lines) && isHunkLine(lines[i]) {
			return fmt.Errorf("%w: hunk longer than its header", errPatchRejected)
		}
		result = append(result, original[next:]...)

		if oldPath != "" && oldPath != newPath {
			delete(files, oldPath)
		}
		if newPath == "" {
			continue
		}
		files[newPath] = []byte(strings.Join(result, ""))
	}
	if touched == 0 {
		return fmt.Errorf("%w: no file diff found", errPatchRejected)
	}
	return nil
}

// patchPath returns a header's path with its first component stripped, "" for /dev/null,
// or an error for a path that would leave the fixture.
func patchPath(header string) (string, error) {
	name := strings.TrimRight(header, "\r\n")
	if tab := strings.IndexByte(name, '\t'); tab >= 0 {
		name = name[:tab] // a timestamp follows the tab
	}
	if name == "/dev/null" {
		return "", nil
	}
	if path.IsAbs(name) {
		return "", fmt.Errorf("%w: path is absolute", errPatchRejected)
	}
	_, rest, found := strings.Cut(name, "/")
	if !found || rest == "" {
		return "", fmt.Errorf("%w: path needs a leading a/ or b/ component", errPatchRejected)
	}
	cleaned := path.Clean(rest)
	if cleaned != rest || cleaned == "." {
		return "", fmt.Errorf("%w: path is not a clean relative path", errPatchRejected)
	}
	for _, part := range strings.Split(cleaned, "/") {
		if part == ".." || strings.HasPrefix(part, ".") {
			return "", fmt.Errorf("%w: path leaves the fixture or names a hidden file", errPatchRejected)
		}
	}
	return cleaned, nil
}

// isHunkLine reports whether line looks like hunk content rather than the start of the
// next file diff or free text.
func isHunkLine(line string) bool {
	if strings.HasPrefix(line, "--- ") {
		return false
	}
	return strings.HasPrefix(line, " ") || strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-")
}

// parseHunkHeader parses "@@ -l[,s] +l[,s] @@" and returns the old start, old count and
// new count.
func parseHunkHeader(line string) (int, int, int, error) {
	fields := strings.Fields(line)
	if len(fields) < 4 || fields[0] != "@@" || fields[3] != "@@" || !strings.HasPrefix(fields[1], "-") || !strings.HasPrefix(fields[2], "+") {
		return 0, 0, 0, fmt.Errorf("%w: malformed hunk header", errPatchRejected)
	}
	oldStart, oldCount, err := parseRange(fields[1][1:])
	if err != nil {
		return 0, 0, 0, err
	}
	_, newCount, err := parseRange(fields[2][1:])
	if err != nil {
		return 0, 0, 0, err
	}
	return oldStart, oldCount, newCount, nil
}

func parseRange(raw string) (int, int, error) {
	startText, countText, hasCount := strings.Cut(raw, ",")
	start, err := strconv.Atoi(startText)
	if err != nil || start < 0 {
		return 0, 0, fmt.Errorf("%w: malformed hunk range", errPatchRejected)
	}
	count := 1
	if hasCount {
		count, err = strconv.Atoi(countText)
		if err != nil || count < 0 {
			return 0, 0, fmt.Errorf("%w: malformed hunk range", errPatchRejected)
		}
	}
	return start, count, nil
}
