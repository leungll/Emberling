package domain

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Fact declarations let the Runtime record and check execution facts from registered
// metadata instead of hard-coded Tool names. A producer Tool states which fact its
// successful result establishes; a consumer Tool states which committed facts must exist
// before one of its Actions may be claimed; a Node Type states which fact types it reads.
// These types only describe the declaration. Whether a pointer lands inside a Tool's
// schemas, and whether every required fact type has a producer, is checked by the Registry,
// which owns the schema compiler and sees every registration.

// FactPointerSource names the JSON document a FactPointer reads: the Tool call's
// arguments or its result. The source is explicit because the same pointer text can be
// valid in both documents and mean different values.
type FactPointerSource string

const (
	FactPointerArguments FactPointerSource = "ARGUMENTS"
	FactPointerResult    FactPointerSource = "RESULT"
)

func (s FactPointerSource) IsValid() bool {
	return s == FactPointerArguments || s == FactPointerResult
}

// FactPointer locates one value inside a Tool call's arguments or result. Pointer is an
// RFC 6901 JSON Pointer and must not be empty: a fact never takes a whole document.
type FactPointer struct {
	Source  FactPointerSource `json:"source"`
	Pointer string            `json:"pointer"`
}

// FactProduction declares the fact a Tool's successful result establishes.
type FactProduction struct {
	FactType string `json:"factType"`
	// SubjectPointer selects the value the fact is about, such as an asset identifier.
	SubjectPointer FactPointer `json:"subjectPointer"`
	// BindArguments maps a binding name to the value recorded under it. A consumer
	// compares a same-named argument of its own call against these bindings.
	BindArguments map[string]FactPointer `json:"bindArguments"`
	// VerdictPointer optionally selects a boolean conclusion carried by the fact.
	VerdictPointer *FactPointer `json:"verdictPointer,omitempty"`
	// BasisFactType optionally names the earlier fact about the same subject that this
	// fact is derived from, so the recorded facts form a provenance chain.
	BasisFactType string `json:"basisFactType,omitempty"`
}

// FactRequirement declares one committed fact a Tool's Action needs before it is claimed.
type FactRequirement struct {
	FactType string `json:"factType"`
	// SubjectArgument is a JSON Pointer into the call's arguments; the required fact must
	// be about the value it selects. Its source is always the arguments.
	SubjectArgument string `json:"subjectArgument"`
	// MatchBindings names bindings whose recorded value must equal the same-named
	// argument of the call.
	MatchBindings []string `json:"matchBindings"`
	// RequireVerdict, when set, requires the fact's verdict to equal this value.
	RequireVerdict *bool `json:"requireVerdict,omitempty"`
}

// ValidateJSONPointer checks the RFC 6901 syntax of a non-empty JSON Pointer: it starts
// with "/" and every "~" is followed by "0" or "1".
func ValidateJSONPointer(pointer string) error {
	if pointer == "" {
		return errors.New("pointer is empty")
	}
	if !strings.HasPrefix(pointer, "/") {
		return fmt.Errorf("pointer %q is not a valid JSON Pointer: it must start with \"/\"", pointer)
	}
	for i := 0; i < len(pointer); i++ {
		if pointer[i] != '~' {
			continue
		}
		if i+1 >= len(pointer) || (pointer[i+1] != '0' && pointer[i+1] != '1') {
			return fmt.Errorf("pointer %q is not a valid JSON Pointer: \"~\" must be followed by \"0\" or \"1\"", pointer)
		}
	}
	return nil
}

// JSONPointerTokens splits a syntactically valid, non-empty JSON Pointer into its
// unescaped reference tokens.
func JSONPointerTokens(pointer string) []string {
	parts := strings.Split(strings.TrimPrefix(pointer, "/"), "/")
	for i, part := range parts {
		parts[i] = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
	}
	return parts
}

func validateFactPointer(field string, pointer FactPointer) []error {
	var errs []error
	if !pointer.Source.IsValid() {
		errs = append(errs, fmt.Errorf("%s: source %q is not %s or %s", field, pointer.Source, FactPointerArguments, FactPointerResult))
	}
	if err := ValidateJSONPointer(pointer.Pointer); err != nil {
		errs = append(errs, fmt.Errorf("%s: %w", field, err))
	}
	return errs
}

// validateToolFactDeclarations checks the shape of a Tool's fact declarations. A Tool
// must not require, or derive its fact from, the fact type it produces itself: such a
// declaration could never be satisfied by an earlier call of another Tool and would let a
// Tool vouch for its own precondition.
func validateToolFactDeclarations(produces *FactProduction, requires []FactRequirement) []error {
	var errs []error
	producedType := ""
	if produces != nil {
		producedType = produces.FactType
		if produces.FactType == "" {
			errs = append(errs, errors.New("produces.factType is empty"))
		}
		errs = append(errs, validateFactPointer("produces.subjectPointer", produces.SubjectPointer)...)
		for _, name := range sortedBindingNames(produces.BindArguments) {
			pointer := produces.BindArguments[name]
			if name == "" {
				errs = append(errs, errors.New("produces.bindArguments: binding name is empty"))
				continue
			}
			errs = append(errs, validateFactPointer(fmt.Sprintf("produces.bindArguments[%q]", name), pointer)...)
		}
		if produces.VerdictPointer != nil {
			errs = append(errs, validateFactPointer("produces.verdictPointer", *produces.VerdictPointer)...)
		}
		if produces.BasisFactType != "" && produces.BasisFactType == produces.FactType {
			errs = append(errs, fmt.Errorf("produces.basisFactType %q: a Tool must not base its fact on the fact type it produces", produces.BasisFactType))
		}
	}
	for i, requirement := range requires {
		field := fmt.Sprintf("requires[%d]", i)
		switch requirement.FactType {
		case "":
			errs = append(errs, fmt.Errorf("%s.factType is empty", field))
		case producedType:
			errs = append(errs, fmt.Errorf("%s.factType %q: a Tool must not require the fact type it produces", field, requirement.FactType))
		}
		if err := ValidateJSONPointer(requirement.SubjectArgument); err != nil {
			errs = append(errs, fmt.Errorf("%s.subjectArgument: %w", field, err))
		}
		seen := make(map[string]struct{}, len(requirement.MatchBindings))
		for _, name := range requirement.MatchBindings {
			if name == "" {
				errs = append(errs, fmt.Errorf("%s.matchBindings: binding name is empty", field))
				continue
			}
			if _, duplicate := seen[name]; duplicate {
				errs = append(errs, fmt.Errorf("%s.matchBindings: duplicate binding %q", field, name))
				continue
			}
			seen[name] = struct{}{}
		}
	}
	return errs
}

// validateFactInputs checks the shape of a Node Type's fact inputs. Whether each fact type
// has a producer Tool is checked by the Registry once every Tool is registered.
func validateFactInputs(factInputs []string) []error {
	var errs []error
	seen := make(map[string]struct{}, len(factInputs))
	for _, factType := range factInputs {
		if factType == "" {
			errs = append(errs, errors.New("factInputs: fact type is empty"))
			continue
		}
		if _, duplicate := seen[factType]; duplicate {
			errs = append(errs, fmt.Errorf("factInputs: duplicate fact type %q", factType))
			continue
		}
		seen[factType] = struct{}{}
	}
	return errs
}

// sortedBindingNames orders binding names so registration errors are reported in a stable
// order.
func sortedBindingNames(bindings map[string]FactPointer) []string {
	names := make([]string, 0, len(bindings))
	for name := range bindings {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
