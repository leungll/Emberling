package readmaterial

import (
	"bufio"
	"errors"
	"io"
	"unicode/utf8"
)

// Per-call bounds. A call returns at most MaxLines lines and at most MaxBytes bytes of
// text; the Tool, not the Runtime, keeps a read within what one request may carry.
const (
	MaxLines = 200
	MaxBytes = 32 << 10
)

// errInvalidUTF8 reports material that is not well-formed UTF-8. It is the only reading
// failure the Tool reports as its own; it never carries the bytes.
var errInvalidUTF8 = errors.New("material is not UTF-8 text")

// lineRange is what one bounded read of a text artifact returned.
type lineRange struct {
	// Text is the returned lines exactly as stored, each with its terminator where the
	// artifact has one, so concatenating consecutive ranges reproduces the artifact.
	Text []byte
	// Lines is the number of lines Text holds.
	Lines int
	// TotalLines is the number of lines of the whole artifact: a terminator ends a line,
	// and a last line without one still counts.
	TotalLines int
	// Truncated is true when a line of the requested range was left out by a bound.
	Truncated bool
}

// readLines streams content once and returns up to lineCount lines from startLine (one
// based), within the per-call bounds. It never holds more than one bufio buffer and the
// returned text in memory. A line is never split: the first line that does not fit in
// MaxBytes, together with every line after it, is left out and the result is truncated,
// since a range is contiguous. Lines beyond the requested range are counted, not kept.
func readLines(content io.Reader, startLine, lineCount int) (lineRange, error) {
	want := min(lineCount, MaxLines)
	endLine := startLine + want - 1
	reader := bufio.NewReaderSize(&utf8Reader{r: content}, 64<<10)

	var out lineRange
	collecting := true
	for lineNo := 1; ; lineNo++ {
		inRange := collecting && lineNo >= startLine && lineNo <= endLine
		var line []byte
		fits := true
		lineLen := 0
		var atEOF bool
		for {
			fragment, err := reader.ReadSlice('\n')
			lineLen += len(fragment)
			if inRange && fits {
				if len(out.Text)+len(line)+len(fragment) > MaxBytes {
					fits, line = false, nil
				} else {
					line = append(line, fragment...)
				}
			}
			if errors.Is(err, bufio.ErrBufferFull) {
				continue
			}
			if errors.Is(err, io.EOF) {
				atEOF = true
				break
			}
			if err != nil {
				return lineRange{}, err
			}
			break
		}
		if atEOF && lineLen == 0 {
			// Nothing after the last terminator: no further line.
			out.TotalLines = lineNo - 1
			break
		}
		if inRange {
			if fits {
				out.Text = append(out.Text, line...)
				out.Lines++
			} else {
				out.Truncated, collecting = true, false
			}
		}
		if atEOF {
			out.TotalLines = lineNo
			break
		}
	}
	if out.Text == nil {
		out.Text = []byte{}
	}
	// Asking for more lines than the line bound is truncation only when the artifact
	// had lines in the range beyond the bound.
	if lineCount > MaxLines && out.TotalLines > endLine {
		out.Truncated = true
	}
	return out, nil
}

// utf8Reader passes bytes through unchanged and fails the read when they are not
// well-formed UTF-8, carrying an incomplete trailing rune across reads so a rune split
// between two reads is not mistaken for an error.
type utf8Reader struct {
	r     io.Reader
	carry []byte
}

func (u *utf8Reader) Read(p []byte) (int, error) {
	n, err := u.r.Read(p)
	if n > 0 {
		chunk := append(u.carry, p[:n]...)
		boundary := len(chunk)
		for i := len(chunk) - 1; i >= 0 && i >= len(chunk)-utf8.UTFMax; i-- {
			if !utf8.RuneStart(chunk[i]) {
				continue
			}
			if !utf8.FullRune(chunk[i:]) {
				boundary = i
			}
			break
		}
		if !utf8.Valid(chunk[:boundary]) {
			return 0, errInvalidUTF8
		}
		u.carry = append([]byte(nil), chunk[boundary:]...)
	}
	if errors.Is(err, io.EOF) && len(u.carry) > 0 {
		return n, errInvalidUTF8
	}
	return n, err
}
