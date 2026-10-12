package sandboxrunner

import (
	"bytes"
	"unicode/utf8"
)

// The patched tree reaches the sandbox inside one environment variable: AX encodes the
// whole Workspace as YAML into it, and the sandbox platform refuses a value longer than
// 32768 characters, after the Workspace was already created. The runner therefore bounds
// the tree twice before creating anything: by its raw content, and by an estimate of the
// encoded value that is never smaller than what AX produces, kept well under the
// platform's limit.
const (
	// maxWorkspaceContentBytes bounds the total content of the patched tree.
	maxWorkspaceContentBytes = 16 << 10
	// maxEncodedWorkspaceChars bounds the estimated length of the encoded Workspace.
	maxEncodedWorkspaceChars = 24576
)

// Constants of the encoding estimate. AX writes the Workspace with four-space
// indentation, every file as a "path" and a "content" member of one list item, and a
// multi-line content as a block literal. They were calibrated on Workspaces measured
// against a running AX, whose encoded lengths the estimate must not undercut; files of
// many short lines grow the most, by the indentation of every line.
const (
	// encodedWorkspaceAllowance covers apiVersion, kind, the metadata other than the name
	// and the atespace, and the spec headings, with room for metadata AX may add.
	encodedWorkspaceAllowance = 512
	// encodedFileAllowance covers one file's "- path: " and "content: |" lines with their
	// indentation, line breaks and the literal's optional indentation and chomping
	// indicators.
	encodedFileAllowance = 40
	// encodedLineAllowance is the indentation and line break of one line of a block
	// literal.
	encodedLineAllowance = 12 + 1
	// quotedCharsPerByte and quotedAllowance bound a value written as a quoted scalar:
	// an escape takes at most four characters per byte, and folding long lines adds
	// less than a fifth more.
	quotedCharsPerByte = 5
	quotedAllowance    = 32
)

// estimatedWorkspaceEncoding estimates, from above, the length in characters of the
// Workspace as AX encodes it into the sandbox's environment. Bytes are counted, which is
// never fewer than characters.
func estimatedWorkspaceEncoding(name, atespace string, files map[string][]byte) int {
	total := encodedWorkspaceAllowance + len(name) + len(atespace)
	for path, content := range files {
		total += encodedFileAllowance + estimatedScalarEncoding([]byte(path)) + estimatedScalarEncoding(content)
	}
	return total
}

// estimatedScalarEncoding bounds one string value. A value of path-like characters only
// is written plain, or quoted when it would read as another type. A value certain to be
// written as a block literal grows only by the indentation of each line. Any other value
// is bounded by the larger of a quoted scalar with escapes and a block literal, since the
// encoder may choose either.
func estimatedScalarEncoding(value []byte) int {
	if pathLike(value) {
		return len(value) + 2
	}
	literal := len(value) + (lineBreaks(value)+1)*encodedLineAllowance
	if writtenAsBlockLiteral(value) {
		return literal
	}
	return max(literal, quotedCharsPerByte*len(value)+quotedAllowance)
}

// pathLike reports whether value holds only letters, digits and . _ / -, which never
// need an escape.
func pathLike(value []byte) bool {
	for _, c := range value {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '_', c == '/', c == '-':
		default:
			return false
		}
	}
	return true
}

// lineBreaks counts the characters YAML treats as line breaks.
func lineBreaks(value []byte) int {
	count := 0
	for len(value) > 0 {
		r, size := utf8.DecodeRune(value)
		value = value[size:]
		switch r {
		case '\n', '\r', 0x85, 0x2028, 0x2029:
			count++
		}
	}
	return count
}

// writtenAsBlockLiteral reports whether a value is certain to be written as a block
// literal: it spans lines, no line ends in a space, and every character is printable as
// is, a tab or a line feed. Other line breaks, control characters, characters outside
// the Basic Multilingual Plane and the byte order mark make the encoder choose by rules
// this estimate does not follow.
func writtenAsBlockLiteral(value []byte) bool {
	if !bytes.Contains(value, []byte("\n")) || bytes.HasSuffix(value, []byte(" ")) || bytes.Contains(value, []byte(" \n")) {
		return false
	}
	for len(value) > 0 {
		r, size := utf8.DecodeRune(value)
		value = value[size:]
		switch {
		case r == '\t' || r == '\n' || r >= 0x20 && r <= 0x7E:
		case r == utf8.RuneError && size == 1, r == 0xFEFF, r == 0x2028, r == 0x2029:
			return false
		case r >= 0xA0 && r <= 0xD7FF, r >= 0xE000 && r <= 0xFFFD:
		default:
			return false
		}
	}
	return true
}
