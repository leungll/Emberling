package service

import (
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
)

func TestRunListCursor_RoundTrip_PreservesBoundaryAndDirection(t *testing.T) {
	digest := runListFilterDigest(store.RunListFilter{WorkflowID: "wf_1"})
	pos := store.RunPosition{StartedAt: time.Date(2026, 8, 3, 12, 0, 0, 123456000, time.UTC), ID: "run_abc"}

	for _, dir := range []store.RunListDirection{store.RunsOlder, store.RunsNewer} {
		gotPos, gotDir, err := decodeRunListCursor(encodeRunListCursor(pos, dir, digest), digest)
		if err != nil {
			t.Fatalf("decode(%v): %v", dir, err)
		}
		if !gotPos.StartedAt.Equal(pos.StartedAt) || gotPos.ID != pos.ID || gotDir != dir {
			t.Errorf("decode(%v) = %+v %v, want %+v %v", dir, gotPos, gotDir, pos, dir)
		}
	}
}

func TestRunListCursor_ForgedOrForeign_RejectedAsQueryError(t *testing.T) {
	digest := runListFilterDigest(store.RunListFilter{WorkflowID: "wf_1"})
	otherDigest := runListFilterDigest(store.RunListFilter{WorkflowID: "wf_2"})
	pos := store.RunPosition{StartedAt: time.Unix(1_790_000_000, 0).UTC(), ID: "run_abc"}
	raw := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

	cases := map[string]string{
		"not base64":         "%%%",
		"not json":           raw("hello"),
		"unknown version":    raw(`{"v":2,"d":"older","t":1,"id":"run_abc","f":"` + digest + `"}`),
		"unknown direction":  raw(`{"v":1,"d":"sideways","t":1,"id":"run_abc","f":"` + digest + `"}`),
		"missing id":         raw(`{"v":1,"d":"older","t":1,"f":"` + digest + `"}`),
		"unknown field":      raw(`{"v":1,"d":"older","t":1,"id":"run_abc","f":"` + digest + `","x":1}`),
		"other filters":      encodeRunListCursor(pos, store.RunsOlder, otherDigest),
		"trailing documents": raw(`{"v":1,"d":"older","t":1,"id":"run_abc","f":"` + digest + `"}{}`),
	}
	for name, cursor := range cases {
		_, _, err := decodeRunListCursor(cursor, digest)
		var qErr *RunListQueryError
		if !errors.As(err, &qErr) {
			t.Errorf("%s: err = %v, want *RunListQueryError", name, err)
		}
	}
}

func TestRunListFilterDigest_DependsOnEveryFilterButNotPageSize(t *testing.T) {
	at := time.Unix(1_790_000_000, 0).UTC()
	base := store.RunListFilter{WorkflowID: "wf_1", Status: domain.RunFailed, StartedFrom: &at, IDPrefix: "run_abcdefgh"}
	later := at.Add(time.Second)
	variants := []store.RunListFilter{
		{Status: base.Status, StartedFrom: base.StartedFrom, IDPrefix: base.IDPrefix},
		{WorkflowID: base.WorkflowID, StartedFrom: base.StartedFrom, IDPrefix: base.IDPrefix},
		{WorkflowID: base.WorkflowID, Status: base.Status, StartedFrom: &later, IDPrefix: base.IDPrefix},
		{WorkflowID: base.WorkflowID, Status: base.Status, StartedFrom: base.StartedFrom},
	}
	for i, v := range variants {
		if runListFilterDigest(v) == runListFilterDigest(base) {
			t.Errorf("variant %d shares the digest of the base filter", i)
		}
	}
}

func TestRunListFilterOf_InvalidValues_RejectedAsQueryError(t *testing.T) {
	cases := map[string]ListRunsRequest{
		"limit above maximum": {Limit: maxRunListLimit + 1},
		"negative limit":      {Limit: -1},
		"unknown status":      {Status: "DONE"},
		"success spelling":    {Status: "SUCCEEDED"},
		"code too short":      {Code: "abc1234"},
		"code too long":       {Code: "a123456789012345678901234567890123456789012345678901234567890123456789"},
		"code punctuation":    {Code: "abcd-1234"},
		"code non-ascii":      {Code: "abcdéfgh"},
	}
	for name, req := range cases {
		_, _, err := runListFilterOf(req)
		var qErr *RunListQueryError
		if !errors.As(err, &qErr) {
			t.Errorf("%s: err = %v, want *RunListQueryError", name, err)
		}
	}
}

func TestRunListFilterOf_ValidValues_NormalizeCodeAndDefaultLimit(t *testing.T) {
	at := time.Date(2026, 8, 3, 12, 0, 0, 999, time.FixedZone("x", 3600))
	filter, limit, err := runListFilterOf(ListRunsRequest{
		WorkflowID: "wf_1", Status: "PAUSED", StartedAfter: &at, Code: "ABCDEF12",
	})
	if err != nil {
		t.Fatalf("runListFilterOf: %v", err)
	}
	if limit != defaultRunListLimit {
		t.Errorf("limit = %d, want default %d", limit, defaultRunListLimit)
	}
	if filter.IDPrefix != "run_abcdef12" {
		t.Errorf("IDPrefix = %q, want run_abcdef12", filter.IDPrefix)
	}
	if filter.Status != domain.RunPaused || filter.WorkflowID != "wf_1" {
		t.Errorf("filter = %+v", filter)
	}
	if filter.StartedFrom == nil || !filter.StartedFrom.Equal(at.Truncate(time.Microsecond)) || filter.StartedFrom.Location() != time.UTC {
		t.Errorf("StartedFrom = %v, want %v in UTC at microsecond precision", filter.StartedFrom, at)
	}
}
