package api

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/service"
)

// Error codes from the interface contract. Each constant is used at exactly the
// call site(s) named in the route table, never inferred generically.
const (
	codeValidationFailed          = "VALIDATION_FAILED"
	codeDefinitionNotFound        = "DEFINITION_NOT_FOUND"
	codeAssetNotFound             = "ASSET_NOT_FOUND"
	codeRunNotFound               = "RUN_NOT_FOUND"
	codeNodeRunNotFound           = "NODE_RUN_NOT_FOUND"
	codeVersionConflict           = "VERSION_CONFLICT"
	codeRuntimeBindingUnavailable = "RUNTIME_BINDING_UNAVAILABLE"
	codePayloadTooLarge           = "PAYLOAD_TOO_LARGE"
	codeDependencyUnavailable     = "DEPENDENCY_UNAVAILABLE"
	codeInternal                  = "INTERNAL"
	codeNotFoundRoute             = "NOT_FOUND"
	codeMethodNotAllowed          = "METHOD_NOT_ALLOWED"
	codeInvalidCallbackCredential = "INVALID_CALLBACK_CREDENTIAL"
	codeInvalidCallbackPayload    = "INVALID_CALLBACK_PAYLOAD"
)

// status422Codes names the runtime.ValidationError codes the interface contract groups
// under HTTP 422 (semantically invalid Definition): an invalid graph shape, not an invalid
// config value. Every other code (config schema and semantic instance failures) is HTTP
// 400. This must match internal/runtime/errors.go's own two const blocks exactly; it is
// duplicated here (rather than imported as a set) because runtime deliberately exposes
// codes as plain string constants, not a classification API.
var status422Codes = map[string]bool{
	runtime.CodeDAGHasCycle:             true,
	runtime.CodeIncompatibleEdge:        true,
	runtime.CodeUnknownNodeType:         true,
	runtime.CodeUnknownPort:             true,
	runtime.CodeMissingRequiredInput:    true,
	runtime.CodeAmbiguousInput:          true,
	runtime.CodeOutputNodeCount:         true,
	runtime.CodeOutputNodeNotUniqueSink: true,
	runtime.CodeNodeUnreachableToOutput: true,
	runtime.CodeInputKeyRequired:        true,
	runtime.CodeDuplicateInputKey:       true,
}

// statusForValidationErrors classifies a compile failure by its first ValidationError's
// code. Compile stops at the first stage producing any error (runtime.CompileError's own
// doc comment), so every error in one result shares one stage and therefore one HTTP
// family; looking at only errs[0] is not an approximation.
func statusForValidationErrors(errs []runtime.ValidationError) int {
	if len(errs) > 0 && !status422Codes[errs[0].Code] {
		return http.StatusBadRequest
	}
	return http.StatusUnprocessableEntity
}

func firstCode(errs []runtime.ValidationError, fallback string) string {
	if len(errs) > 0 {
		return errs[0].Code
	}
	return fallback
}

// validationErrorDTO mirrors runtime.ValidationError but always includes "path", even
// when empty, matching studio/src/api/types.ts's ValidationError.path (a required
// field). runtime.ValidationError itself tags Path `json:"path,omitempty"`, which would
// otherwise drop the key entirely for a whole-graph error such as DAG_HAS_CYCLE that
// never sets Path; normalizing that here is a transport-layer decision, not a change to
// runtime's own contract.
type validationErrorDTO struct {
	Code    string `json:"code"`
	Path    string `json:"path"`
	NodeID  string `json:"nodeId,omitempty"`
	Message string `json:"message"`
}

func toValidationErrorDTOs(errs []runtime.ValidationError) []validationErrorDTO {
	out := make([]validationErrorDTO, len(errs))
	for i, e := range errs {
		out[i] = validationErrorDTO{Code: e.Code, Path: e.Path, NodeID: e.NodeID, Message: e.Message}
	}
	return out
}

type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

func writeErrorEnvelope(w http.ResponseWriter, status int, code, message string, details any) {
	writeJSON(w, status, errorEnvelope{Error: errorBody{Code: code, Message: message, Details: details}})
}

// writeError is the single place that maps a service/runtime/domain error to the HTTP
// error envelope the interface contract defines. notFoundCode names the
// resource-specific 404 code to use when err is (or wraps) domain.ErrNotFound; every
// other classification is inferred from err's own type, never from the call site.
//
// No branch below ever includes node config, Run input/output, SQL or a secret in the
// response: structured details are limited to the same bounded ValidationError list a
// direct /definitions/validate call already returns, and the catch-all branch logs the
// real error server-side only.
func writeError(w http.ResponseWriter, logger *slog.Logger, err error, notFoundCode string) {
	var compileErr *service.CompileFailedError
	if errors.As(err, &compileErr) {
		status := statusForValidationErrors(compileErr.Result.Errors)
		writeErrorEnvelope(w, status, firstCode(compileErr.Result.Errors, codeValidationFailed),
			"definition failed validation",
			map[string]any{"errors": toValidationErrorDTOs(compileErr.Result.Errors)})
		return
	}

	var inputErr *service.RunInputInvalidError
	if errors.As(err, &inputErr) {
		// The interface contract groups runtime.CodeValidationFailed under HTTP 400
		// (invalid request content); execution.go's own doc comment on
		// RunInputInvalidError says the same thing explicitly. This is HTTP 400, not 422.
		writeErrorEnvelope(w, http.StatusBadRequest, firstCode(inputErr.Errors, codeValidationFailed),
			"run input failed validation against the definition's frozen runInputSchema",
			map[string]any{"errors": toValidationErrorDTOs(inputErr.Errors)})
		return
	}

	var registryErr *service.RegistryResolutionError
	if errors.As(err, &registryErr) {
		writeErrorEnvelope(w, http.StatusConflict, codeRuntimeBindingUnavailable,
			"the registry can no longer resolve this frozen definition version", nil)
		return
	}

	if errors.Is(err, domain.ErrVersionConflict) {
		writeErrorEnvelope(w, http.StatusConflict, codeVersionConflict,
			"the definition has already been saved past baseVersion; reload the latest version and retry", nil)
		return
	}

	if errors.Is(err, domain.ErrNotFound) {
		code := notFoundCode
		if code == "" {
			code = codeNotFoundRoute
		}
		writeErrorEnvelope(w, http.StatusNotFound, code, "the requested resource does not exist", nil)
		return
	}

	// The interface contract maps an oversized Asset upload to 413 and every invalid
	// request content to 400; an unsupported image type and content that does not match
	// what the caller declared are both the latter, so they reuse VALIDATION_FAILED
	// rather than adding a public code the interface spec does not define.
	if errors.Is(err, domain.ErrAssetTooLarge) {
		writeErrorEnvelope(w, http.StatusRequestEntityTooLarge, codePayloadTooLarge,
			"asset content exceeds the maximum upload size", nil)
		return
	}

	if errors.Is(err, domain.ErrUnsupportedAssetMediaType) {
		writeErrorEnvelope(w, http.StatusBadRequest, codeValidationFailed,
			"asset media type is not supported", nil)
		return
	}

	if errors.Is(err, domain.ErrAssetContentMismatch) {
		writeErrorEnvelope(w, http.StatusBadRequest, codeValidationFailed,
			"uploaded content does not match the declared upload", nil)
		return
	}

	var badReq *badRequestError
	if errors.As(err, &badReq) {
		writeErrorEnvelope(w, http.StatusBadRequest, codeValidationFailed, badReq.message, nil)
		return
	}

	// Everything else is an unclassified failure (an infrastructure error, typically):
	// log the real error server-side only, and answer with a generic message.
	logger.Error("api: unhandled error", "error", err)
	writeErrorEnvelope(w, http.StatusInternalServerError, codeInternal, "internal server error", nil)
}
