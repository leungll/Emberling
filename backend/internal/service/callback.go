package service

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/store"
)

// CallbackConfig carries the settings the async callback use cases need. It is a service
// value rather than internal/config.Callback so this package keeps depending on domain,
// registry, runtime and store only; cmd/emberling reveals the Secret once at startup and
// hands the bytes over.
type CallbackConfig struct {
	// BaseURL is the externally reachable Backend origin a Provider calls back into.
	BaseURL string
	// SigningSecret is the HMAC key the Attempt-scoped callback token is signed with.
	SigningSecret []byte
	// PendingTTL bounds how long an early callback with no binding yet is retained.
	PendingTTL time.Duration
}

// CallbackPath is the single callback endpoint every dispatched Attempt is told to call.
const CallbackPath = "/api/callbacks"

// ErrInvalidCallbackCredential reports a callback whose token failed verification. The
// caller must persist nothing for such a delivery -- an unverified sender must not be able
// to fill the pending_callbacks table.
var ErrInvalidCallbackCredential = errors.New("execution: invalid callback credential")

// ErrNoCallbackBinding reports that no Callback Binding routes the external task id. It is
// the signal HandleCallback turns into a Pending Callback and the Reconciler treats as
// "nothing to resume".
var ErrNoCallbackBinding = errors.New("execution: no callback binding for external task")

// CallbackPayloadRejectedError reports that the registered Executor could not interpret
// the callback payload. Per 06 §1.6 this is deliberately not a state change: the NodeRun
// stays WAITING_CALLBACK and the Provider still gets a 200, because re-delivering a
// payload this Executor cannot parse would not help.
type CallbackPayloadRejectedError struct {
	ExternalTaskID string
	Err            error
}

func (e *CallbackPayloadRejectedError) Error() string {
	return fmt.Sprintf("execution: callback payload rejected for external task %s: %v", e.ExternalTaskID, e.Err)
}

func (e *CallbackPayloadRejectedError) Unwrap() error { return e.Err }

// ResumeNode is the parameter struct for ExecutionService.ResumeNode.
type ResumeNode struct {
	// ExternalTaskID routes the delivery through its Callback Binding.
	ExternalTaskID string
	// Payload is the raw Provider body handed to the Executor's OnCallback.
	Payload json.RawMessage
	// Source records which path delivered the result; it decides whether
	// NODE_CALLBACK_RECEIVED is written and what completionSource NODE_COMPLETED carries.
	Source domain.CompletionSource
	// ConsumePending marks this delivery as the replay of a stored early callback, which
	// must be consumed exactly once in the same transaction that acts on it.
	ConsumePending bool
	// PayloadHash carries the hash recorded when the delivery first arrived. It is only
	// set when replaying a stored early callback: pending_callbacks.payload stores JSONB,
	// so the body read back is semantically identical but not byte-identical, and
	// re-hashing it would neither match the stored payload_hash nor identify what the
	// Provider actually sent. Empty means "hash Payload".
	PayloadHash string
}

// hash identifies this delivery for the Pending Callback guard and for the bounded
// NODE_CALLBACK_RECEIVED payload.
func (r ResumeNode) hash() string {
	if r.PayloadHash != "" {
		return r.PayloadHash
	}
	return payloadHash(r.Payload)
}

// ResumeOutcome is ResumeNode's result. Duplicate reports that another resume, timeout or
// poll already took the completion right, so this delivery wrote nothing and consumed no
// Event seq. FailureSource is only meaningful when Failed is true; it is not persisted in
// NODE_FAILED (docs/05-data-model.md §2.3 gives that payload error + attemptNo only).
type ResumeOutcome struct {
	RunID         string
	NodeRunID     string
	AttemptID     string
	Duplicate     bool
	Failed        bool
	FailureSource domain.FailureSource
}

// HandleCallback is the parameter struct for ExecutionService.HandleCallback.
type HandleCallback struct {
	Token          string
	ExternalTaskID string
	Payload        json.RawMessage
}

// CallbackOutcome is HandleCallback's result. Pending means the delivery arrived before
// its Callback Binding committed and was stored for the dispatcher (or the Reconciler) to
// pick up; Accepted means it entered the resume use case.
type CallbackOutcome struct {
	Accepted  bool
	Pending   bool
	Duplicate bool
	RunID     string
	NodeRunID string
}

// ResumeNode is the single idempotent resume use case shared by callback intake, Provider
// polling and the Reconciler (invariant #5). It never calls the Provider or the Executor
// while holding the Run lock: OnCallback runs between the routing reads and the single
// state-changing transaction (invariant #4, 06 §1.6 "恢复事务内不执行节点、不调用 Provider").
func (s *ExecutionService) ResumeNode(ctx context.Context, req ResumeNode) (ResumeOutcome, error) {
	var (
		binding  domain.CallbackBinding
		attempt  domain.NodeAttempt
		nodeRun  domain.NodeRun
		run      domain.Run
		def      domain.Definition
		resolved bool
	)

	// Phase 1: routing facts, read from one consistent snapshot and no lock.
	err := s.deps.UoW.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		binding, err = tx.CallbackBindings().GetByExternalTaskID(ctx, req.ExternalTaskID)
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				return ErrNoCallbackBinding
			}
			return err
		}
		if binding.TargetType != domain.CallbackTargetNodeAttempt {
			// A TOOL_ATTEMPT Binding is resumed by resumeToolAttempt below; any other
			// target type is refused rather than guessed.
			return nil
		}
		attempt, err = tx.NodeAttempts().Get(ctx, binding.TargetID)
		if err != nil {
			return err
		}
		if attempt.Status != domain.NodeAttemptDispatched {
			// The Binding points at an Attempt that is no longer awaiting a result: a
			// duplicate, a late delivery, or a callback for an Attempt a newer retry
			// replaced. Nothing is written and no Event seq is consumed (09 §3.2).
			return nil
		}
		nodeRun, err = tx.NodeRuns().Get(ctx, attempt.NodeRunID)
		if err != nil {
			return err
		}
		run, err = tx.Runs().Get(ctx, nodeRun.RunID)
		if err != nil {
			return err
		}
		def, err = tx.Definitions().GetVersion(ctx, run.WorkflowID, run.DefinitionVersion)
		if err != nil {
			return err
		}
		resolved = true
		return nil
	})
	if err != nil {
		return ResumeOutcome{}, err
	}
	switch binding.TargetType {
	case domain.CallbackTargetNodeAttempt:
	case domain.CallbackTargetToolAttempt:
		// An asynchronous Agent Tool enters the same resume use case (invariant #5); only
		// the target it resolves and the transaction that commits the outcome differ.
		return s.resumeToolAttempt(ctx, req, binding)
	default:
		return ResumeOutcome{}, fmt.Errorf("execution: callback binding %s targets unknown type %s", binding.ID, binding.TargetType)
	}
	if !resolved {
		return ResumeOutcome{
			NodeRunID: attempt.NodeRunID,
			AttemptID: attempt.ID,
			Duplicate: true,
		}, nil
	}

	executor, err := s.asyncExecutor(nodeRun.NodeType, run)
	if err != nil {
		return ResumeOutcome{}, err
	}
	node, ok := nodeByID(def, nodeRun.NodeID)
	if !ok {
		return ResumeOutcome{}, fmt.Errorf("execution: definition %s v%d no longer declares node %q", def.WorkflowID, def.Version, nodeRun.NodeID)
	}
	config, err := decodeConfig(node.Config)
	if err != nil {
		return ResumeOutcome{}, err
	}

	// Phase 2: interpret the payload outside every transaction and every lock.
	output, cbErr := executor.OnCallback(ctx, registry.NodeAsyncState{
		RunID:     run.ID,
		NodeRunID: nodeRun.ID,
		AttemptNo: attempt.AttemptNo,
		ExternalTask: registry.ExternalTask{
			ProviderID:     binding.ProviderID,
			ExternalTaskID: binding.ExternalTaskID,
		},
		Config: config,
	}, req.Payload)

	failureSource := domain.FailureCallback
	if req.Source == domain.CompletionProviderPoll {
		failureSource = domain.FailureProviderPoll
	}

	// Phase 3: one transaction that changes state and writes its Events together.
	var providerFailure *registry.ProviderFailure
	switch {
	case cbErr != nil && errors.As(cbErr, &providerFailure):
		result, err := s.failNode(ctx, failNodeParams{
			attemptID:   attempt.ID,
			execError:   providerFailure.Err,
			uncertain:   false,
			source:      failureSource,
			fromAttempt: domain.NodeAttemptDispatched,
			fromNodeRun: domain.NodeRunWaitingCallback,
			// 06 §2.2: a Provider that reports its task failed resolves the Attempt and the
			// NodeRun; MVP does not re-dispatch an external task that already waited.
			terminal:       true,
			consumePending: s.pendingToConsume(req),
			payloadHash:    req.hash(),
		})
		if err != nil {
			return ResumeOutcome{}, err
		}
		return ResumeOutcome{
			RunID:         run.ID,
			NodeRunID:     nodeRun.ID,
			AttemptID:     attempt.ID,
			Duplicate:     result.duplicate,
			Failed:        !result.duplicate,
			FailureSource: failureSource,
		}, nil
	case cbErr != nil:
		// Not a Provider failure: the payload could not be interpreted. Leave the NodeRun
		// WAITING_CALLBACK and persist nothing.
		return ResumeOutcome{
			RunID:     run.ID,
			NodeRunID: nodeRun.ID,
			AttemptID: attempt.ID,
		}, &CallbackPayloadRejectedError{ExternalTaskID: req.ExternalTaskID, Err: cbErr}
	}

	result, err := s.completeNode(ctx, completeNodeParams{
		attemptID:         attempt.ID,
		output:            output,
		fromAttempt:       domain.NodeAttemptDispatched,
		fromNodeRun:       domain.NodeRunWaitingCallback,
		completionSource:  req.Source,
		callbackBindingID: binding.ID,
		consumePending:    s.pendingToConsume(req),
		payloadHash:       req.hash(),
	})
	if err != nil {
		return ResumeOutcome{}, err
	}
	return ResumeOutcome{
		RunID:     run.ID,
		NodeRunID: nodeRun.ID,
		AttemptID: attempt.ID,
		Duplicate: result.duplicate,
	}, nil
}

// pendingToConsume returns the external task id whose Pending Callback row this resume
// must consume exactly once, or "" when the delivery did not come from a stored one.
func (s *ExecutionService) pendingToConsume(req ResumeNode) string {
	if !req.ConsumePending {
		return ""
	}
	return req.ExternalTaskID
}

// asyncExecutor resolves the registered AsyncNodeExecutor for a Node Type. A missing or
// incompatible registration fails explicitly and changes no state: CLAUDE.md forbids
// substituting the closest available implementation.
func (s *ExecutionService) asyncExecutor(nodeType string, run domain.Run) (registry.AsyncNodeExecutor, error) {
	resolutionErr := func(code, message string) error {
		return &RegistryResolutionError{
			WorkflowID: run.WorkflowID,
			Version:    run.DefinitionVersion,
			Underlying: &runtime.CompileError{
				Stage:  runtime.StageSemantics,
				Errors: []runtime.ValidationError{{Code: code, Message: message}},
			},
		}
	}

	reg, ok := s.deps.Nodes.Get(nodeType)
	if !ok {
		return nil, resolutionErr(runtime.CodeUnknownNodeType, fmt.Sprintf("node type %q is not registered", nodeType))
	}
	binding, ok := reg.Binding.(registry.ExecutorBinding)
	if !ok {
		return nil, resolutionErr(runtime.CodeUnknownNodeType, fmt.Sprintf("node type %q is not an EXECUTOR binding", nodeType))
	}
	async, ok := binding.Executor.(registry.AsyncNodeExecutor)
	if !ok {
		return nil, resolutionErr(runtime.CodeUnknownNodeType, fmt.Sprintf("node type %q does not implement AsyncNodeExecutor", nodeType))
	}
	return async, nil
}

// HandleCallback is the callback intake the HTTP handler calls. It verifies the credential
// before touching the database, stores an early delivery whose Callback Binding has not
// committed yet, and otherwise enters the shared resume use case.
func (s *ExecutionService) HandleCallback(ctx context.Context, req HandleCallback) (CallbackOutcome, error) {
	now := s.deps.Clock.Now()
	claims, err := verifyCallbackToken(s.deps.Callback.SigningSecret, req.Token, now)
	if err != nil {
		return CallbackOutcome{}, err
	}

	var binding domain.CallbackBinding
	var bound bool
	err = s.deps.UoW.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		found, err := tx.CallbackBindings().GetByExternalTaskID(ctx, req.ExternalTaskID)
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				return nil
			}
			return err
		}
		binding, bound = found, true
		return nil
	})
	if err != nil {
		return CallbackOutcome{}, err
	}

	if !bound {
		// 06 §1.6 / 05 §1.7: the dispatch transaction may not have committed yet. The
		// delivery is stored under its external task id, keyed by the same token hash the
		// Attempt row carries, so the dispatcher can tell its own callback apart from a
		// credential that belongs to a different Attempt.
		var duplicate bool
		if err := s.deps.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
			var err error
			duplicate, err = tx.PendingCallbacks().Record(ctx, domain.PendingCallback{
				ExternalTaskID:    req.ExternalTaskID,
				Payload:           req.Payload,
				PayloadHash:       payloadHash(req.Payload),
				CallbackTokenHash: hashCallbackToken(req.Token),
				ReceivedAt:        now,
				ExpiresAt:         now.Add(s.deps.Callback.PendingTTL),
			})
			return err
		}); err != nil {
			return CallbackOutcome{}, err
		}
		return CallbackOutcome{Pending: true, Duplicate: duplicate}, nil
	}

	// The credential is Attempt-scoped: a valid token for another Attempt must not be able
	// to complete this one.
	if claims.AttemptID != binding.TargetID {
		return CallbackOutcome{}, ErrInvalidCallbackCredential
	}

	outcome, err := s.ResumeNode(ctx, ResumeNode{
		ExternalTaskID: req.ExternalTaskID,
		Payload:        req.Payload,
		Source:         domain.CompletionCallback,
	})
	if err != nil {
		return CallbackOutcome{}, err
	}
	return CallbackOutcome{
		Accepted:  true,
		Duplicate: outcome.Duplicate,
		RunID:     outcome.RunID,
		NodeRunID: outcome.NodeRunID,
	}, nil
}

// -----------------------------------------------------------------------------------
// Callback token
// -----------------------------------------------------------------------------------

// callbackToken is the verified content of a callback credential.
type callbackToken struct {
	AttemptID string
	ExpiresAt time.Time
}

const callbackTokenVersion = "v1"

// issueCallbackToken mints the Attempt-scoped credential handed to a Provider. It is
// self-contained on purpose: verification needs the signing secret only, so an unroutable
// or forged callback is rejected without a database lookup. The plaintext exists in
// registry.CallbackContext and nowhere else -- only sha256(token) is persisted.
func issueCallbackToken(secret []byte, attemptID string, expiresAt time.Time) (string, error) {
	if len(secret) == 0 {
		return "", errors.New("execution: callback signing secret is not configured")
	}
	if attemptID == "" {
		return "", errors.New("execution: callback token needs an attempt id")
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("execution: generate callback token nonce: %w", err)
	}
	// A zero expiresAt means "no expiry" and is encoded as 0: it is the Attempt of a Node
	// whose ExecutionPolicy sets no timeout, so there is no deadline to derive a lifetime
	// from and inventing one would kill a legitimate long-running Provider task. The
	// Attempt's DISPATCHED status is the lifetime instead -- verification still binds the
	// token to one attempt id, and the resume path rejects anything the Binding no longer
	// routes (06 §1.6).
	var expiryMillis int64
	if !expiresAt.IsZero() {
		expiryMillis = expiresAt.UTC().UnixMilli()
	}
	body := strings.Join([]string{
		callbackTokenVersion,
		attemptID,
		base64.RawURLEncoding.EncodeToString(nonce),
		strconv.FormatInt(expiryMillis, 10),
	}, ".")
	encoded := base64.RawURLEncoding.EncodeToString([]byte(body))
	return encoded + "." + signCallbackBody(secret, encoded), nil
}

func signCallbackBody(secret []byte, encodedBody string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(encodedBody))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// verifyCallbackToken checks the signature in constant time and then the expiry. Every
// rejection is the same error: a caller must not be able to tell a forged signature from
// an expired one, and neither may cause a database write.
func verifyCallbackToken(secret []byte, token string, now time.Time) (callbackToken, error) {
	if len(secret) == 0 || token == "" {
		return callbackToken{}, ErrInvalidCallbackCredential
	}
	encodedBody, signature, ok := strings.Cut(token, ".")
	if !ok {
		return callbackToken{}, ErrInvalidCallbackCredential
	}
	expected := signCallbackBody(secret, encodedBody)
	if !hmac.Equal([]byte(signature), []byte(expected)) {
		return callbackToken{}, ErrInvalidCallbackCredential
	}
	raw, err := base64.RawURLEncoding.DecodeString(encodedBody)
	if err != nil {
		return callbackToken{}, ErrInvalidCallbackCredential
	}
	parts := strings.Split(string(raw), ".")
	if len(parts) != 4 || parts[0] != callbackTokenVersion || parts[1] == "" {
		return callbackToken{}, ErrInvalidCallbackCredential
	}
	expiryMillis, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil {
		return callbackToken{}, ErrInvalidCallbackCredential
	}
	if expiryMillis == 0 {
		return callbackToken{AttemptID: parts[1]}, nil
	}
	expiresAt := time.UnixMilli(expiryMillis).UTC()
	if !now.Before(expiresAt) {
		return callbackToken{}, ErrInvalidCallbackCredential
	}
	return callbackToken{AttemptID: parts[1], ExpiresAt: expiresAt}, nil
}

// hashCallbackToken is the only form of a callback token that may be persisted.
func hashCallbackToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// payloadHash identifies a callback body without storing it twice; it is what makes a
// re-delivery recognisable as the same delivery.
func payloadHash(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}
