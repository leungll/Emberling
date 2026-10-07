package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/service"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestWriteError_MapsEachClassifiedErrorToItsDocumentedStatusAndCode is a table test over
// writeError's own doc comment: it is the single function every handler calls to map a
// service/runtime/domain error to the HTTP error contract,
// so this test is the one place that pins each branch's status/code pair.
func TestWriteError_MapsEachClassifiedErrorToItsDocumentedStatusAndCode(t *testing.T) {
	cases := []struct {
		name         string
		err          error
		notFoundCode string
		wantStatus   int
		wantCode     string
	}{
		{
			name: "CompileFailedError with a 422-family code (graph shape)",
			err: &service.CompileFailedError{Result: service.ValidateResult{
				Errors: []runtime.ValidationError{{Code: runtime.CodeDAGHasCycle, Message: "cycle"}},
			}},
			wantStatus: 422,
			wantCode:   runtime.CodeDAGHasCycle,
		},
		{
			name: "CompileFailedError with a 400-family code (config/semantic validation)",
			err: &service.CompileFailedError{Result: service.ValidateResult{
				Errors: []runtime.ValidationError{{Code: runtime.CodeValidationFailed, Message: "bad config"}},
			}},
			wantStatus: 400,
			wantCode:   runtime.CodeValidationFailed,
		},
		{
			name: "RunInputInvalidError is always 400, per its own doc comment, never 422",
			err: &service.RunInputInvalidError{
				Errors: []runtime.ValidationError{{Code: runtime.CodeValidationFailed, Path: "/document", Message: "required"}},
			},
			wantStatus: 400,
			wantCode:   runtime.CodeValidationFailed,
		},
		{
			name:       "RegistryResolutionError maps to 409 RUNTIME_BINDING_UNAVAILABLE",
			err:        &service.RegistryResolutionError{WorkflowID: "wf_1", Version: 1},
			wantStatus: 409,
			wantCode:   codeRuntimeBindingUnavailable,
		},
		{
			name:       "domain.ErrVersionConflict maps to 409 VERSION_CONFLICT",
			err:        domain.ErrVersionConflict,
			wantStatus: 409,
			wantCode:   codeVersionConflict,
		},
		{
			name:         "domain.ErrNotFound uses the caller-supplied notFoundCode",
			err:          domain.ErrNotFound,
			notFoundCode: codeDefinitionNotFound,
			wantStatus:   404,
			wantCode:     codeDefinitionNotFound,
		},
		{
			name:       "domain.ErrNotFound with no notFoundCode falls back to NOT_FOUND",
			err:        domain.ErrNotFound,
			wantStatus: 404,
			wantCode:   codeNotFoundRoute,
		},
		{
			name:       "badRequestError maps to 400 VALIDATION_FAILED",
			err:        &badRequestError{message: "bad input"},
			wantStatus: 400,
			wantCode:   codeValidationFailed,
		},
		{
			name:       "an unclassified error maps to 500 INTERNAL and never leaks its own message",
			err:        errors.New("pgx: connection reset by peer, password=hunter2"),
			wantStatus: 500,
			wantCode:   codeInternal,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeError(rec, discardLogger(), tc.err, tc.notFoundCode)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body=%s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			var env errorEnvelope
			if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
				t.Fatalf("decode error envelope: %v (body=%s)", err, rec.Body.String())
			}
			if env.Error.Code != tc.wantCode {
				t.Errorf("error.code = %q, want %q", env.Error.Code, tc.wantCode)
			}
			if strings.Contains(rec.Body.String(), "hunter2") || strings.Contains(rec.Body.String(), "password=") {
				t.Errorf("error response leaked what looks like a secret: %s", rec.Body.String())
			}
		})
	}
}

// TestWriteError_CompileFailedError_LooksAtOnlyTheFirstError proves
// statusForValidationErrors' own justification directly: Compile stops at the first stage
// with any error, so every error in one CompileFailedError.Result shares one family: a
// mixed slice (which Compile itself would never actually produce) must still classify by
// errs[0], not by scanning the rest.
func TestWriteError_CompileFailedError_LooksAtOnlyTheFirstError(t *testing.T) {
	err := &service.CompileFailedError{Result: service.ValidateResult{
		Errors: []runtime.ValidationError{
			{Code: runtime.CodeValidationFailed}, // 400 family, listed first
			{Code: runtime.CodeDAGHasCycle},      // 422 family, would win if scanned
		},
	}}
	rec := httptest.NewRecorder()
	writeError(rec, discardLogger(), err, "")
	if rec.Code != 400 {
		t.Fatalf("status = %d, want 400 (classified by errs[0] only)", rec.Code)
	}
}
