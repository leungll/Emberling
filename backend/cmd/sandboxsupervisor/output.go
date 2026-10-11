package main

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
	"unicode/utf8"
)

// maxTestOutputBytes bounds the command output the supervisor reports. It has the same
// value as the runner's limit for the docker backend's container output, so both backends
// hand the runner the same amount of output to count tests from.
const maxTestOutputBytes = 1 << 20

// boundedOutput keeps the first limit bytes written to it and records whether any were
// dropped. The command's standard output and standard error share one instance, so the os/exec
// package calls Write from one goroutine at a time.
type boundedOutput struct {
	limit   int
	data    []byte
	dropped bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	kept := p
	if room := b.limit - len(b.data); len(kept) > room {
		kept = kept[:max(room, 0)]
		b.dropped = true
	}
	b.data = append(b.data, kept...)
	return len(p), nil
}

// reportedOutput returns the longest prefix of the command output that the result may
// carry, and whether anything after it was left out. The output is made valid UTF-8 by
// replacing each invalid byte with U+FFFD, and the prefix ends on a rune boundary and
// encodes as a JSON string of at most maxTestOutputBytes bytes, quotes excluded. Escaping
// can make the encoding several times longer than the text, so the limit applies to the
// encoding; that keeps the whole response body within the limit plus a small fixed
// overhead.
func reportedOutput(raw []byte, dropped bool) (string, bool) {
	if dropped {
		raw = trimPartialRune(raw)
	}
	text := validUTF8(raw)
	if jsonStringLength(text) <= maxTestOutputBytes {
		return text, dropped
	}
	// The encoded length grows monotonically with the prefix, so the longest prefix within
	// the limit is found by binary search over byte offsets snapped down to rune starts.
	cut := sort.Search(len(text)+1, func(n int) bool {
		return jsonStringLength(text[:runeStartAtOrBefore(text, n)]) > maxTestOutputBytes
	}) - 1
	return text[:runeStartAtOrBefore(text, cut)], true
}

// trimPartialRune drops a rune cut short by the output limit, so the cut does not show up
// as a replacement character that the command never wrote.
func trimPartialRune(raw []byte) []byte {
	for i := len(raw) - 1; i >= 0 && i >= len(raw)-utf8.UTFMax; i-- {
		if utf8.RuneStart(raw[i]) {
			if !utf8.FullRune(raw[i:]) {
				return raw[:i]
			}
			break
		}
	}
	return raw
}

// validUTF8 returns raw as a string with each invalid byte replaced by U+FFFD.
func validUTF8(raw []byte) string {
	if utf8.Valid(raw) {
		return string(raw)
	}
	var builder strings.Builder
	builder.Grow(len(raw))
	for len(raw) > 0 {
		r, size := utf8.DecodeRune(raw)
		if r == utf8.RuneError && size == 1 {
			builder.WriteRune(utf8.RuneError)
		} else {
			builder.Write(raw[:size])
		}
		raw = raw[size:]
	}
	return builder.String()
}

// runeStartAtOrBefore returns the largest rune boundary of the valid UTF-8 text at or
// before n.
func runeStartAtOrBefore(text string, n int) int {
	for n > 0 && n < len(text) && !utf8.RuneStart(text[n]) {
		n--
	}
	return n
}

// jsonStringLength is the length of s encoded by the result's JSON encoder, which does not
// escape HTML, excluding the surrounding quotes.
func jsonStringLength(s string) int {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	// Encoding a string cannot fail.
	_ = encoder.Encode(s)
	return len(bytes.TrimSuffix(buf.Bytes(), []byte("\n"))) - 2
}
