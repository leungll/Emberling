// Package changerequest implements the `create_change_request` Tool: it renders the change
// request an Agent proposes for the workspace's current patch as Markdown, keeps that text
// as a text Execution Artifact, and returns the change request record for the Runtime to
// persist.
//
// The Agent names the patch by digest and supplies the summary, impact, rollback plan and
// the decision basis: statements it relied on, each pointing at an item of a Turn's Context
// Manifest. When the patch was reviewed by the independent review model, the Agent may
// attach that review by citing its fact and list the review findings it left unresolved, so
// the approver sees them. Everything else in the request comes from committed facts the
// Runtime resolved in the claim transaction: the workspace view names the repository, base
// commit, current patch and deploy target, and the required facts carry the verification
// the patch passed and the cited review. The Runtime refuses a digest that is not the
// current patch and a patch without a passing verification before this Tool is called, and
// checks every manifest reference and the review citation; the Tool only renders and never
// looks a fact up itself.
//
// The executor computes a result; it writes no database row. The change request becomes a
// fact when the Runtime records it in the result transaction, under the CHANGE_REQUEST
// record kind this registration declares. Writing the content Artifact leaves only
// unreferenced content behind if that transaction never commits, so the Tool declares no
// side effect and a SAFE repeat: a call interrupted by a crash is handed back to the Agent
// as interrupted work and simply made again. The content digest is the SHA-256 of the
// Markdown bytes, the same digest the Artifact is addressed by, and the Runtime makes two
// requests with the same content inside one Agent Run converge on one record.
package changerequest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// ToolName is this Tool's stable Registry name, stored in an Agent's Tool allowlist.
const ToolName = "create_change_request"

// RequiredFactType is the fact type a change request rests on: the verification of the
// patch it describes, about the patch digest, with a true verdict. The producer registers
// the same fact type name; the Registry refuses this Tool when no registered Tool produces
// it.
const RequiredFactType = "patch_verified"

// MaxTextRunes bounds each free-text argument: the summary, the impact and the rollback
// plan.
const MaxTextRunes = 4000

// MaxStatementRunes bounds one decision basis statement.
const MaxStatementRunes = 1000

// MaxManifestRefRunes bounds one manifest reference. References are record identifiers or
// repository paths, never content.
const MaxManifestRefRunes = 256

// ReviewFactType is the fact type a cited review must have; the Runtime checks the citation
// against the Run's facts before this Tool is called.
const ReviewFactType = "patch_reviewed"

// Bounds of the unresolved review findings the Agent lists for the approver.
const (
	MaxUnresolvedFindings     = 16
	MaxUnresolvedFindingRunes = 500
)

const inputSchema = `{
  "type": "object",
  "properties": {
    "patchDigest": {"type": "string", "minLength": 64, "maxLength": 64},
    "summary": {"type": "string", "minLength": 1, "maxLength": 4000},
    "impact": {"type": "string", "minLength": 1, "maxLength": 4000},
    "rollbackPlan": {"type": "string", "minLength": 1, "maxLength": 4000},
    "reviewFactId": {"type": "string", "minLength": 1, "maxLength": 128},
    "unresolvedFindings": {
      "type": "array",
      "maxItems": 16,
      "items": {"type": "string", "minLength": 1, "maxLength": 500}
    },
    "decisionBasis": {
      "type": "array",
      "minItems": 1,
      "maxItems": 32,
      "items": {
        "type": "object",
        "properties": {
          "statement": {"type": "string", "minLength": 1, "maxLength": 1000},
          "turnNo": {"type": "integer", "minimum": 1},
          "manifestRef": {"type": "string", "minLength": 1, "maxLength": 256}
        },
        "required": ["statement", "turnNo", "manifestRef"],
        "additionalProperties": false
      }
    }
  },
  "required": ["patchDigest", "summary", "impact", "rollbackPlan", "decisionBasis"],
  "additionalProperties": false
}`

// outputSchema admits changeRequestId and existing without requiring them: the Tool cannot
// know the record's identifier, which the Runtime allocates when it records the request in
// the result transaction and adds to the result there, together with whether the request
// converged on an earlier record with the same content.
const outputSchema = `{
  "type": "object",
  "properties": {
    "patchDigest": {"type": "string", "minLength": 64, "maxLength": 64},
    "verificationFactId": {"type": "string", "minLength": 1},
    "contentArtifactId": {"type": "string", "minLength": 1},
    "contentDigest": {"type": "string", "minLength": 64, "maxLength": 64},
    "basisCount": {"type": "integer", "minimum": 1},
    "reviewFactId": {"type": "string", "minLength": 1},
    "changeRequestId": {"type": "string", "minLength": 1},
    "existing": {"type": "boolean"}
  },
  "required": ["patchDigest", "verificationFactId", "contentArtifactId", "contentDigest", "basisCount"],
  "additionalProperties": false
}`

// ArtifactTextStore is the port this Tool writes the rendered content through. The store
// returns the content-addressed reference the result declares; the Runtime commits the
// reference's metadata with the result.
type ArtifactTextStore interface {
	WriteText(ctx context.Context, r io.Reader) (domain.ArtifactRef, error)
}

// Registration returns the `create_change_request` ToolRegistration keeping its content in
// artifacts.
//
// The Tool uses the workspace and names the patch it describes at /patchDigest, which the
// Runtime compares with the current patch before the call. It requires a RequiredFactType
// fact about that digest with a true verdict, and asks for an unmet requirement to be
// returned to the Agent, which then verifies the patch and calls again. See the package
// comment for why a Tool that leads to a persisted record declares no side effect.
func Registration(artifacts ArtifactTextStore) registry.ToolRegistration {
	verdict := true
	return registry.ToolRegistration{
		Metadata: domain.ToolMetadata{
			Name:          ToolName,
			Title:         "Create change request",
			Description:   "Create the change request for the workspace's current, verified patch: summary, impact, rollback plan and the decision basis linked to the turns' context manifests",
			InputSchema:   json.RawMessage(inputSchema),
			OutputSchema:  json.RawMessage(outputSchema),
			SideEffect:    domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
			ExecutionKind: domain.ToolExecutionSync,
			Requires: []domain.FactRequirement{{
				FactType:        RequiredFactType,
				SubjectArgument: "/patchDigest",
				MatchBindings:   []string{},
				RequireVerdict:  &verdict,
			}},
			Records:                     []domain.ToolRecordKind{domain.ToolRecordChangeRequest},
			CountsTowardGenerationLimit: false,
			UsesWorkspace:               true,
			WorkspacePatchDigestPath:    "/patchDigest",
			ReturnUnmetRequirement:      true,
			// The summary, impact, rollback plan and decision basis are the change request's
			// content, read through its Artifact; only the identifiers are shown.
			Display: []domain.ToolDisplayField{
				{Source: domain.FactPointerArguments, Pointer: "/patchDigest", Title: "Patch digest"},
				{Source: domain.FactPointerResult, Pointer: "/changeRequestId", Title: "Change request"},
				{Source: domain.FactPointerResult, Pointer: "/basisCount", Title: "Decision basis items"},
				{Source: domain.FactPointerResult, Pointer: "/existing", Title: "Already recorded"},
			},
		},
		Executor: New(artifacts),
	}
}

// Executor implements registry.ToolExecutor for `create_change_request`.
type Executor struct {
	artifacts ArtifactTextStore
}

// New returns an Executor keeping content in artifacts.
func New(artifacts ArtifactTextStore) *Executor {
	return &Executor{artifacts: artifacts}
}

type arguments struct {
	PatchDigest        string          `json:"patchDigest"`
	Summary            string          `json:"summary"`
	Impact             string          `json:"impact"`
	RollbackPlan       string          `json:"rollbackPlan"`
	ReviewFactID       string          `json:"reviewFactId"`
	UnresolvedFindings []string        `json:"unresolvedFindings"`
	DecisionBasis      json.RawMessage `json:"decisionBasis"`
}

// result is the Tool's own result. The Runtime adds the record identifier when it commits
// the request. ReviewFactID is present only when the request attaches a review.
type result struct {
	PatchDigest        string `json:"patchDigest"`
	VerificationFactID string `json:"verificationFactId"`
	ContentArtifactID  string `json:"contentArtifactId"`
	ContentDigest      string `json:"contentDigest"`
	BasisCount         int    `json:"basisCount"`
	ReviewFactID       string `json:"reviewFactId,omitempty"`
}

// Execute renders the change request, stores it as a text Artifact and returns the record.
func (e *Executor) Execute(ctx context.Context, action registry.ToolAction) (registry.ToolExecutionResult, error) {
	if e.artifacts == nil {
		return registry.ToolExecutionResult{}, toolError("artifact content storage is not configured")
	}
	if action.Workspace == nil {
		return registry.ToolExecutionResult{}, toolError("the call carries no workspace view")
	}
	var args arguments
	if err := json.Unmarshal(action.Arguments, &args); err != nil {
		return registry.ToolExecutionResult{}, toolError("arguments are not an object")
	}
	if args.PatchDigest == "" || args.Summary == "" || args.Impact == "" || args.RollbackPlan == "" {
		return registry.ToolExecutionResult{}, toolError("arguments lack the patch digest, summary, impact or rollback plan")
	}
	// The Runtime refused a stale digest before this call; the check is repeated here so
	// this Tool can never describe a patch other than the workspace's current one.
	if args.PatchDigest != action.Workspace.PatchDigest {
		return registry.ToolExecutionResult{}, toolError("patchDigest is not the workspace's current patch")
	}
	basis, err := domain.DecodeChangeRequestBasis(args.DecisionBasis)
	if err != nil {
		return registry.ToolExecutionResult{}, toolError(err.Error())
	}
	verification, found := verificationOf(action.RequiredFacts, args.PatchDigest)
	if !found {
		return registry.ToolExecutionResult{}, toolError("the call carries no passing " + RequiredFactType + " fact about the patch")
	}
	var review *domain.ExecutionFact
	if args.ReviewFactID != "" {
		// The Runtime resolved and checked the citation before this call; a call that
		// cites a review it does not carry is refused rather than rendered without it.
		cited, found := reviewOf(action.RequiredFacts, args.ReviewFactID, args.PatchDigest)
		if !found {
			return registry.ToolExecutionResult{}, toolError("the call carries no " + ReviewFactType + " fact matching the cited review")
		}
		review = &cited
	}
	if len(args.UnresolvedFindings) > MaxUnresolvedFindings {
		return registry.ToolExecutionResult{}, toolError("too many unresolved findings")
	}
	for _, item := range args.UnresolvedFindings {
		if strings.TrimSpace(item) == "" || utf8.RuneCountInString(item) > MaxUnresolvedFindingRunes {
			return registry.ToolExecutionResult{}, toolError("an unresolved finding is empty or too long")
		}
	}

	content := render(renderInput{
		workspace:          *action.Workspace,
		summary:            args.Summary,
		impact:             args.Impact,
		rollbackPlan:       args.RollbackPlan,
		verification:       verification,
		review:             review,
		unresolvedFindings: args.UnresolvedFindings,
		basis:              basis,
	})
	sum := sha256.Sum256([]byte(content))
	contentDigest := hex.EncodeToString(sum[:])

	ref, err := e.artifacts.WriteText(ctx, strings.NewReader(content))
	if err != nil {
		// The store's error names no path; the content is not quoted either.
		return registry.ToolExecutionResult{}, fmt.Errorf("%s: store change request content: %w", ToolName, err)
	}
	if ref.SHA256 != contentDigest {
		return registry.ToolExecutionResult{}, toolError("artifact storage reported a digest other than the content's")
	}

	canonicalBasis, err := json.Marshal(basis)
	if err != nil {
		return registry.ToolExecutionResult{}, fmt.Errorf("%s: encode decision basis: %w", ToolName, err)
	}
	output, err := json.Marshal(result{
		PatchDigest:        args.PatchDigest,
		VerificationFactID: verification.ID,
		ContentArtifactID:  ref.ArtifactID,
		ContentDigest:      contentDigest,
		BasisCount:         len(basis),
		ReviewFactID:       args.ReviewFactID,
	})
	if err != nil {
		return registry.ToolExecutionResult{}, fmt.Errorf("%s: encode result: %w", ToolName, err)
	}
	return registry.ToolExecutionResult{
		Kind: registry.ToolResultCompleted,
		Result: &registry.ToolResult{
			Output:    output,
			Artifacts: []domain.ArtifactRef{ref},
			ChangeRequest: &registry.ChangeRequest{
				PatchDigest:        args.PatchDigest,
				VerificationFactID: verification.ID,
				ContentArtifact:    ref,
				ContentDigest:      contentDigest,
				DecisionBasis:      canonicalBasis,
				ReviewFactID:       args.ReviewFactID,
			},
		},
	}, nil
}

// verificationOf selects, from the facts the Runtime resolved for the call, the passing
// verification of patchDigest.
func verificationOf(facts []domain.ExecutionFact, patchDigest string) (domain.ExecutionFact, bool) {
	for _, fact := range facts {
		if fact.FactType == RequiredFactType && fact.SubjectRef == patchDigest && fact.Verdict != nil && *fact.Verdict {
			return fact, true
		}
	}
	return domain.ExecutionFact{}, false
}

// reviewOf selects, from the facts the Runtime resolved for the call, the cited review of
// patchDigest.
func reviewOf(facts []domain.ExecutionFact, factID, patchDigest string) (domain.ExecutionFact, bool) {
	for _, fact := range facts {
		if fact.ID == factID && fact.FactType == ReviewFactType && fact.SubjectRef == patchDigest {
			return fact, true
		}
	}
	return domain.ExecutionFact{}, false
}

// renderInput is everything the Markdown rendering reads.
type renderInput struct {
	workspace    registry.WorkspaceView
	summary      string
	impact       string
	rollbackPlan string
	verification domain.ExecutionFact
	// review is the cited review fact, nil when the request attaches none.
	review             *domain.ExecutionFact
	unresolvedFindings []string
	basis              []domain.ChangeRequestBasis
}

// render produces the change request as Markdown. The output is a pure function of its
// input, so the same request always yields the same bytes and the same content digest. The
// patch itself is named by digest and not copied: the approval view reads the workspace's
// patch record by that digest.
func render(in renderInput) string {
	var b strings.Builder
	b.WriteString("# Change request\n\n")

	b.WriteString("## Patch\n\n")
	writeField(&b, "Repository", in.workspace.RepositoryID)
	writeField(&b, "Base commit", in.workspace.BaseCommit)
	writeField(&b, "Patch digest", in.workspace.PatchDigest)
	writeField(&b, "Workspace patch", fmt.Sprintf("%d", in.workspace.PatchSeq))
	touched := domain.WorkspacePatchTouchedPaths(in.workspace.Patch)
	if len(touched) == 0 {
		writeField(&b, "Touched paths", "none")
	} else {
		writeField(&b, "Touched paths", strings.Join(touched, ", "))
	}
	if target := in.workspace.DeployTarget; target != nil {
		writeField(&b, "Deploy target", target.Service+" ("+target.Environment+")")
	}
	b.WriteString("\n")

	writeSection(&b, "Summary", in.summary)
	writeSection(&b, "Impact", in.impact)
	writeSection(&b, "Rollback plan", in.rollbackPlan)

	b.WriteString("## Verification\n\n")
	writeField(&b, "Fact", in.verification.ID)
	writeField(&b, "Fact type", in.verification.FactType)
	writeField(&b, "Subject", in.verification.SubjectRef)
	writeField(&b, "Verdict", "passed")
	for _, name := range sortedBindingNames(in.verification.Binding) {
		writeField(&b, name, bindingValue(in.verification.Binding, name))
	}
	b.WriteString("\n")

	// The review section names the review fact and its conclusion, and lists what the Agent
	// left unresolved, so the approver can weigh the second opinion. The opinion text itself
	// stays in the Artifact the fact binds.
	b.WriteString("## Review\n\n")
	if in.review == nil {
		b.WriteString("No independent review is attached to this request.\n")
	} else {
		writeField(&b, "Fact", in.review.ID)
		writeField(&b, "Fact type", in.review.FactType)
		writeField(&b, "Subject", in.review.SubjectRef)
		switch {
		case in.review.Verdict == nil:
			writeField(&b, "Blocking findings", "not recorded")
		case *in.review.Verdict:
			writeField(&b, "Blocking findings", "none")
		default:
			writeField(&b, "Blocking findings", "yes")
		}
		for _, name := range sortedBindingNames(in.review.Binding) {
			writeField(&b, name, bindingValue(in.review.Binding, name))
		}
		b.WriteString("\nUnresolved findings:\n\n")
		if len(in.unresolvedFindings) == 0 {
			b.WriteString("- none\n")
		}
		for _, item := range in.unresolvedFindings {
			b.WriteString("- " + strings.TrimSpace(item) + "\n")
		}
	}
	b.WriteString("\n")

	b.WriteString("## Decision basis\n\n")
	for i, item := range in.basis {
		fmt.Fprintf(&b, "%d. %s (turn %d, manifest item `%s`)\n", i+1, strings.TrimSpace(item.Statement), item.TurnNo, item.ManifestRef)
	}
	return b.String()
}

func writeField(b *strings.Builder, name, value string) {
	fmt.Fprintf(b, "- %s: `%s`\n", name, value)
}

func writeSection(b *strings.Builder, title, text string) {
	b.WriteString("## " + title + "\n\n")
	b.WriteString(strings.TrimSpace(text))
	b.WriteString("\n\n")
}

// sortedBindingNames lists the names of a fact's bindings in a stable order; an unreadable
// binding contributes nothing.
func sortedBindingNames(binding json.RawMessage) []string {
	var values map[string]any
	if err := json.Unmarshal(binding, &values); err != nil {
		return nil
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// bindingValue renders one binding value: a string verbatim, anything else as JSON.
func bindingValue(binding json.RawMessage, name string) string {
	var values map[string]json.RawMessage
	if err := json.Unmarshal(binding, &values); err != nil {
		return ""
	}
	var text string
	if err := json.Unmarshal(values[name], &text); err == nil {
		return text
	}
	return string(values[name])
}

// toolError builds a failure from fixed labels only, so no argument or content can reach
// Trace or a log through it.
func toolError(reason string) error {
	return fmt.Errorf("%s: %w", ToolName, errors.New(reason))
}
