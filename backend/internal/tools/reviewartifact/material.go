package reviewartifact

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"
)

// This file owns what the review reads: the canonical timeline, the sentence-per-line
// transcript, and the excerpt the two select from it. Nothing here touches a model.

// Read bounds. A timeline holds at most maxTimelineSegments sentence ranges, the bound
// the timeline check accepts, so its canonical JSON stays far under maxTimelineBytes; a
// transcript of a talk the workload supports is tens of kilobytes, so maxTranscriptBytes
// leaves room without letting one call hold an arbitrary artifact in memory.
const (
	maxTimelineBytes     = 256 << 10
	maxTranscriptBytes   = 1 << 20
	maxTimelineSegments  = 200
	maxTranscriptLines   = 20000
	maxTranscriptColumns = 4
)

// contextSentences is how many sentences before and after each selected range the
// model reads beside the selection, so it can tell whether a sentence standing alone
// keeps the meaning its neighbours give it.
const contextSentences = 2

var sha256HexRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// timeline is what the review reads of the canonical timeline the media service
// produced: the sentence ranges it selected and the transcript it was computed over.
// Other members are left alone.
type timeline struct {
	TranscriptSha256 string            `json:"transcriptSha256"`
	Segments         []timelineSegment `json:"segments"`
}

// timelineSegment is one selected range, from sentence From to sentence To inclusive.
type timelineSegment struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// sentence is one transcript line: the sentence id and its text. The times the line
// also carries are not read; the review judges words, not cuts.
type sentence struct {
	ID   string
	Text string
}

// excerptLine is one sentence the model reads, selected by the timeline or shown as
// context around a selection.
type excerptLine struct {
	// Index is the sentence's position in the transcript, so the rendering can mark a
	// gap between two windows.
	Index    int
	ID       string
	Text     string
	Selected bool
}

// readBounded reads content up to limit bytes and fails when there is more, so a call
// never holds an artifact past its bound.
func readBounded(content io.Reader, limit int) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(content, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, errTooLarge
	}
	return data, nil
}

// decodeTimeline accepts a canonical timeline naming a transcript digest and between one
// and maxTimelineSegments ranges with both ends named.
func decodeTimeline(data []byte) (timeline, error) {
	var tl timeline
	if err := json.Unmarshal(data, &tl); err != nil {
		return timeline{}, errors.New("timeline is not a JSON object")
	}
	if !sha256HexRe.MatchString(tl.TranscriptSha256) {
		return timeline{}, errors.New("timeline names no transcript digest")
	}
	if len(tl.Segments) == 0 || len(tl.Segments) > maxTimelineSegments {
		return timeline{}, fmt.Errorf("timeline has %d segments, want 1 to %d", len(tl.Segments), maxTimelineSegments)
	}
	for i, segment := range tl.Segments {
		if segment.From == "" || segment.To == "" {
			return timeline{}, fmt.Errorf("segment %d names no sentence range", i)
		}
	}
	return tl, nil
}

// decodeTranscript reads the transcript material: one sentence per line, written as the
// sentence id, its start, its end and its text separated by tabs. A line that is not
// shaped so, a repeated id, or text that is not UTF-8 makes the transcript unusable.
func decodeTranscript(data []byte) ([]sentence, error) {
	if !utf8.Valid(data) {
		return nil, errors.New("transcript is not UTF-8 text")
	}
	text := strings.TrimSuffix(string(data), "\n")
	if text == "" {
		return nil, errors.New("transcript is empty")
	}
	lines := strings.Split(text, "\n")
	if len(lines) > maxTranscriptLines {
		return nil, errTooLarge
	}
	sentences := make([]sentence, 0, len(lines))
	seen := make(map[string]struct{}, len(lines))
	for i, line := range lines {
		columns := strings.SplitN(line, "\t", maxTranscriptColumns)
		if len(columns) != maxTranscriptColumns || columns[0] == "" {
			return nil, fmt.Errorf("transcript line %d is not a sentence line", i+1)
		}
		if _, dup := seen[columns[0]]; dup {
			return nil, fmt.Errorf("transcript line %d repeats a sentence id", i+1)
		}
		seen[columns[0]] = struct{}{}
		sentences = append(sentences, sentence{ID: columns[0], Text: columns[3]})
	}
	return sentences, nil
}

// excerpt selects, in transcript order, every sentence a segment covers and up to
// contextSentences sentences on each side of each segment, clamped at the transcript's
// edges. A sentence covered by any segment is selected even where another segment's
// context reaches it. A segment whose ends are not transcript sentences in order is an
// error: the timeline does not belong to this transcript.
func excerpt(sentences []sentence, segments []timelineSegment) ([]excerptLine, error) {
	index := make(map[string]int, len(sentences))
	for i, s := range sentences {
		index[s.ID] = i
	}
	const (
		none = iota
		context
		selected
	)
	marks := make([]int, len(sentences))
	for n, segment := range segments {
		from, okFrom := index[segment.From]
		to, okTo := index[segment.To]
		if !okFrom || !okTo || from > to {
			return nil, fmt.Errorf("segment %d does not name an ordered sentence range of the transcript", n)
		}
		for i := max(from-contextSentences, 0); i < from; i++ {
			marks[i] = max(marks[i], context)
		}
		for i := from; i <= to; i++ {
			marks[i] = selected
		}
		for i := to + 1; i <= min(to+contextSentences, len(sentences)-1); i++ {
			marks[i] = max(marks[i], context)
		}
	}
	var lines []excerptLine
	for i, mark := range marks {
		if mark == none {
			continue
		}
		lines = append(lines, excerptLine{Index: i, ID: sentences[i].ID, Text: sentences[i].Text, Selected: mark == selected})
	}
	return lines, nil
}
