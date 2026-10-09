package runtime

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// Execution error codes for fact-protected Actions. They are bounded ExecutionError.Code
// strings, not database enums. A precondition or generation-limit rejection fails the
// Action in its claim transaction before any Tool Attempt exists, so no external call is
// made; a missing basis fact fails the Action in the Tool result transaction.
const (
	CodePreconditionUnmet      = "PRECONDITION_UNMET"
	CodeGenerationLimitReached = "GENERATION_LIMIT_REACHED"
	CodeFactBasisMissing       = "FACT_BASIS_MISSING"
)

// ProducedFact is the fact a successful Tool result establishes under the Tool's
// registered production declaration, before it is given an identity and persisted.
type ProducedFact struct {
	FactType   string
	SubjectRef string
	// Binding holds each declared binding as its canonical string form.
	Binding map[string]string
	// Verdict is nil when the declaration has no verdict pointer.
	Verdict *bool
	// BasisFactType is copied from the declaration; the result transaction resolves it
	// to the newest same-subject fact of that type in the Run.
	BasisFactType string
}

// BindingJSON renders the binding as a JSON object with sorted keys, {} when empty, so
// the persisted form does not depend on map iteration order.
func (f ProducedFact) BindingJSON() (json.RawMessage, error) {
	obj := make(map[string]any, len(f.Binding))
	for name, value := range f.Binding {
		obj[name] = value
	}
	encoded, err := encodeCanonical(obj)
	if err != nil {
		return nil, fmt.Errorf("encode binding of fact type %q: %w", f.FactType, err)
	}
	return encoded, nil
}

// FactExtractionReason classifies why a declared value could not be read.
type FactExtractionReason string

const (
	FactValueMissing    FactExtractionReason = "VALUE_MISSING"
	FactValueWrongType  FactExtractionReason = "VALUE_WRONG_TYPE"
	FactDocumentInvalid FactExtractionReason = "DOCUMENT_INVALID"
	FactPointerInvalid  FactExtractionReason = "POINTER_INVALID"
)

// FactExtractionError reports a production declaration that does not match the Tool's
// arguments or result. It names the fact type, the declaration field and the pointer,
// which all come from registered metadata, and never echoes a document value: arguments
// and results may contain signed URLs.
type FactExtractionError struct {
	FactType string
	Field    string
	Source   domain.FactPointerSource
	Pointer  string
	Reason   FactExtractionReason
}

func (e *FactExtractionError) Error() string {
	return fmt.Sprintf("extract fact %q: %s (%s %s): %s", e.FactType, e.Field, e.Source, e.Pointer, e.Reason)
}

// ExtractProducedFact reads the fact a successful Tool call establishes. The subject must
// be a non-empty string and a declared verdict must be a boolean. Binding values are
// stringified canonically: a string is kept as-is; a number, boolean, object or array
// becomes its canonical JSON text (numbers keep their literal text, object keys are
// sorted). A null or absent value is an error, so a binding never records "missing" as a
// value.
func ExtractProducedFact(production domain.FactProduction, arguments, result json.RawMessage) (ProducedFact, error) {
	docs := factDocuments{arguments: arguments, result: result}
	fact := ProducedFact{
		FactType:      production.FactType,
		BasisFactType: production.BasisFactType,
		Binding:       make(map[string]string, len(production.BindArguments)),
	}

	subject, err := docs.resolve(production.FactType, "subjectPointer", production.SubjectPointer)
	if err != nil {
		return ProducedFact{}, err
	}
	subjectText, ok := subject.(string)
	if !ok || subjectText == "" {
		return ProducedFact{}, docs.fail(production.FactType, "subjectPointer", production.SubjectPointer, FactValueWrongType)
	}
	fact.SubjectRef = subjectText

	for _, name := range sortedFactBindingNames(production.BindArguments) {
		pointer := production.BindArguments[name]
		field := fmt.Sprintf("bindArguments[%q]", name)
		value, err := docs.resolve(production.FactType, field, pointer)
		if err != nil {
			return ProducedFact{}, err
		}
		text, ok, err := canonicalFactString(value)
		if err != nil || !ok {
			return ProducedFact{}, docs.fail(production.FactType, field, pointer, FactValueWrongType)
		}
		fact.Binding[name] = text
	}

	if production.VerdictPointer != nil {
		value, err := docs.resolve(production.FactType, "verdictPointer", *production.VerdictPointer)
		if err != nil {
			return ProducedFact{}, err
		}
		verdict, ok := value.(bool)
		if !ok {
			return ProducedFact{}, docs.fail(production.FactType, "verdictPointer", *production.VerdictPointer, FactValueWrongType)
		}
		fact.Verdict = &verdict
	}
	return fact, nil
}

// factDocuments decodes each source document at most once.
type factDocuments struct {
	arguments, result json.RawMessage
	decoded           map[domain.FactPointerSource]any
	decodeFailed      map[domain.FactPointerSource]bool
}

func (d *factDocuments) fail(factType, field string, pointer domain.FactPointer, reason FactExtractionReason) error {
	return &FactExtractionError{FactType: factType, Field: field, Source: pointer.Source, Pointer: pointer.Pointer, Reason: reason}
}

func (d *factDocuments) resolve(factType, field string, pointer domain.FactPointer) (any, error) {
	if domain.ValidateJSONPointer(pointer.Pointer) != nil {
		return nil, d.fail(factType, field, pointer, FactPointerInvalid)
	}
	var raw json.RawMessage
	switch pointer.Source {
	case domain.FactPointerArguments:
		raw = d.arguments
	case domain.FactPointerResult:
		raw = d.result
	default:
		return nil, d.fail(factType, field, pointer, FactPointerInvalid)
	}
	if d.decoded == nil {
		d.decoded = make(map[domain.FactPointerSource]any, 2)
		d.decodeFailed = make(map[domain.FactPointerSource]bool, 2)
	}
	doc, seen := d.decoded[pointer.Source]
	if !seen && !d.decodeFailed[pointer.Source] {
		var err error
		doc, err = decodeFactDocument(raw)
		if err != nil {
			d.decodeFailed[pointer.Source] = true
		} else {
			d.decoded[pointer.Source] = doc
		}
	}
	if d.decodeFailed[pointer.Source] {
		return nil, d.fail(factType, field, pointer, FactDocumentInvalid)
	}
	value, found := lookupJSONPointer(doc, domain.JSONPointerTokens(pointer.Pointer))
	if !found || value == nil {
		return nil, d.fail(factType, field, pointer, FactValueMissing)
	}
	return value, nil
}

// decodeFactDocument decodes one JSON document, keeping numbers as their literal text.
// Decoder errors are not wrapped by callers because they can quote document bytes.
func decodeFactDocument(raw json.RawMessage) (any, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, errors.New("document is empty")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("document has trailing data")
	}
	return doc, nil
}

// lookupJSONPointer walks unescaped JSON Pointer tokens through a decoded document. An
// array token must be a canonical non-negative decimal index inside the array.
func lookupJSONPointer(doc any, tokens []string) (any, bool) {
	current := doc
	for _, token := range tokens {
		switch node := current.(type) {
		case map[string]any:
			next, ok := node[token]
			if !ok {
				return nil, false
			}
			current = next
		case []any:
			if token == "" || (len(token) > 1 && token[0] == '0') {
				return nil, false
			}
			index, err := strconv.Atoi(token)
			if err != nil || index < 0 || index >= len(node) {
				return nil, false
			}
			current = node[index]
		default:
			return nil, false
		}
	}
	return current, true
}

// canonicalFactString renders a decoded JSON value as the string a binding records and
// compares. ok is false for null.
func canonicalFactString(value any) (string, bool, error) {
	switch v := value.(type) {
	case nil:
		return "", false, nil
	case string:
		return v, true, nil
	default:
		encoded, err := encodeCanonical(v)
		if err != nil {
			return "", false, err
		}
		return string(encoded), true, nil
	}
}

// SettingsDigest returns "sha256:" followed by the lowercase hex SHA-256 of the canonical
// JSON of a settings object: numbers keep their literal text and object keys are sorted
// at every depth, so key order and whitespace do not change the digest. Tools compute it
// over the settings they actually applied and return it in their result, where a
// production declaration can bind it; the Runtime never derives it from arguments on a
// Tool's behalf.
func SettingsDigest(settings json.RawMessage) (string, error) {
	doc, err := decodeFactDocument(settings)
	if err != nil {
		return "", errors.New("settings digest: settings are not a single valid JSON document")
	}
	if _, ok := doc.(map[string]any); !ok {
		return "", errors.New("settings digest: settings must be a JSON object")
	}
	encoded, err := encodeCanonical(doc)
	if err != nil {
		return "", fmt.Errorf("settings digest: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// RequirementFailureReason classifies an unmet fact requirement.
type RequirementFailureReason string

const (
	// RequirementNoFact: no committed fact of the required type is about the subject.
	RequirementNoFact RequirementFailureReason = "NO_FACT"
	// RequirementBindingMismatch: a same-subject fact exists but a named binding differs
	// from the same-named argument of the call.
	RequirementBindingMismatch RequirementFailureReason = "BINDING_MISMATCH"
	// RequirementVerdictMismatch: a same-subject fact with matching bindings exists but
	// its verdict is not the required one.
	RequirementVerdictMismatch RequirementFailureReason = "VERDICT_MISMATCH"
	// RequirementArgumentMissing: the call's arguments do not carry a string subject, or
	// lack a value for a binding the requirement compares.
	RequirementArgumentMissing RequirementFailureReason = "ARGUMENT_MISSING"
)

// RequirementMatch is the committed fact that satisfies a requirement.
type RequirementMatch struct {
	Fact domain.ExecutionFact
}

// RequirementFailure describes an unmet requirement with bounded, secret-free details:
// the fact type, the subject reference (an asset identifier, never a URL), the reason and
// the binding name when one is involved. Argument values other than the subject are
// never included.
type RequirementFailure struct {
	FactType string
	Subject  string
	Reason   RequirementFailureReason
	Binding  string
}

// Details returns the failure as an ExecutionError details map.
func (f RequirementFailure) Details() map[string]string {
	details := map[string]string{"factType": f.FactType, "reason": string(f.Reason)}
	if f.Subject != "" {
		details["subject"] = f.Subject
	}
	if f.Binding != "" {
		details["binding"] = f.Binding
	}
	return details
}

// ExecutionError renders the failure as the PRECONDITION_UNMET error an Action fails
// with in its claim transaction.
func (f RequirementFailure) ExecutionError() domain.ExecutionError {
	message := fmt.Sprintf("required fact %q is not satisfied: %s", f.FactType, f.Reason)
	if f.Binding != "" {
		message = fmt.Sprintf("%s on binding %q", message, f.Binding)
	}
	// A map of strings always marshals.
	details, _ := json.Marshal(f.Details())
	return domain.ExecutionError{Code: CodePreconditionUnmet, Message: message, Details: details}
}

// MatchRequirement checks one fact requirement against committed facts of the Run. The
// subject is the string at SubjectArgument in the call's arguments. A candidate satisfies
// the requirement when its fact type matches, its subject equals the subject, every
// MatchBindings name's recorded value equals the canonical string of the top-level
// argument of that name, and, when RequireVerdict is set, its verdict equals it.
// Candidates are passed newest first and that order is kept: the first satisfying
// candidate is returned. When none satisfies, the failure describes the newest
// same-subject candidate, so a fact about another subject never explains a rejection.
func MatchRequirement(req domain.FactRequirement, arguments json.RawMessage, candidates []domain.ExecutionFact) (RequirementMatch, *RequirementFailure) {
	doc, err := decodeFactDocument(arguments)
	if err != nil {
		return RequirementMatch{}, &RequirementFailure{FactType: req.FactType, Reason: RequirementArgumentMissing}
	}
	if domain.ValidateJSONPointer(req.SubjectArgument) != nil {
		return RequirementMatch{}, &RequirementFailure{FactType: req.FactType, Reason: RequirementArgumentMissing}
	}
	subjectValue, _ := lookupJSONPointer(doc, domain.JSONPointerTokens(req.SubjectArgument))
	subject, isString := subjectValue.(string)
	if !isString || subject == "" {
		return RequirementMatch{}, &RequirementFailure{FactType: req.FactType, Reason: RequirementArgumentMissing}
	}

	expected := make(map[string]string, len(req.MatchBindings))
	if len(req.MatchBindings) > 0 {
		object, _ := doc.(map[string]any)
		for _, name := range req.MatchBindings {
			text, ok, err := canonicalFactString(object[name])
			if err != nil || !ok {
				return RequirementMatch{}, &RequirementFailure{FactType: req.FactType, Subject: subject, Reason: RequirementArgumentMissing, Binding: name}
			}
			expected[name] = text
		}
	}

	var first *RequirementFailure
	for _, candidate := range candidates {
		if candidate.FactType != req.FactType || candidate.SubjectRef != subject {
			continue
		}
		failure := checkCandidate(req, subject, expected, candidate)
		if failure == nil {
			return RequirementMatch{Fact: candidate}, nil
		}
		if first == nil {
			first = failure
		}
	}
	if first != nil {
		return RequirementMatch{}, first
	}
	return RequirementMatch{}, &RequirementFailure{FactType: req.FactType, Subject: subject, Reason: RequirementNoFact}
}

func checkCandidate(req domain.FactRequirement, subject string, expected map[string]string, candidate domain.ExecutionFact) *RequirementFailure {
	if len(req.MatchBindings) > 0 {
		var recorded map[string]any
		if err := json.Unmarshal(candidate.Binding, &recorded); err != nil {
			recorded = nil
		}
		for _, name := range req.MatchBindings {
			value, ok := recorded[name].(string)
			if !ok || value != expected[name] {
				return &RequirementFailure{FactType: req.FactType, Subject: subject, Reason: RequirementBindingMismatch, Binding: name}
			}
		}
	}
	if req.RequireVerdict != nil && (candidate.Verdict == nil || *candidate.Verdict != *req.RequireVerdict) {
		return &RequirementFailure{FactType: req.FactType, Subject: subject, Reason: RequirementVerdictMismatch}
	}
	return nil
}

// GenerationLimitReached reports whether another counted generation call would exceed
// the Agent Run's frozen limit. A nil limit means unlimited.
func GenerationLimitReached(limit *int, attemptsSoFar int) bool {
	return limit != nil && attemptsSoFar >= *limit
}

// sortedFactBindingNames orders binding names so extraction reports the same error for
// the same declaration on every call.
func sortedFactBindingNames(bindings map[string]domain.FactPointer) []string {
	names := make([]string, 0, len(bindings))
	for name := range bindings {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
