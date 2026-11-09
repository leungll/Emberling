# Emberling Coding Guide

This public file defines how coding agents work in the Emberling repository. It is an implementation guide, not a product or architecture specification.

Emberling is an Agent Runtime Platform built around a durable, AI-native Execution Runtime. Workflow Studio is a thin client used to define and observe executions; it is not the product category and must not own Runtime behavior.

Public repository content must be self-contained. Do not add links to private design material or make the build, tests, examples, or contribution workflow depend on files that are not published.

## Authority and scope

Implement behavior established by the current code, database migrations, public interfaces, acceptance tests, or the maintainer-approved task. Maintainer-provided internal contracts, when present in the working environment, take precedence over implementation convenience and examples.

When a `docs/private/` directory is present in the working tree, read `docs/private/AGENTS.md` and `docs/private/ENGINEERING.md` in full before implementation, then read the numbered design documents that own the area being changed. These files own the shared internal contracts. Current requirements define acceptance; the applicable approved technical proposal and task contract define new mechanisms and explicitly supersede older rules within their scope. Numbered documents retain ownership of unaffected rules and must be synchronized during implementation. Code establishes implemented behavior; test and run records establish verified behavior. A dated warning about older documents is not a permanent blanket invalidation. Do not expose or link to internal documents from public repository content.

Code, tests, comments, error messages, and fixtures must not cite `docs/private/` files by number, filename, or section: no `06 §2.6`, no `06-execution-model.md`, and no `docs/private/...` paths. A comment states the invariant or rule it relies on in its own words, so the code is understandable without access to `docs/private/`. When provenance helps, name the concept in domain terms, such as "the async resume contract" or "the callback authentication rule", rather than a document location. Do not reference rules or invariants by number, such as `invariant #5` or `rule #8`, and do not use planning milestone labels such as `M5 slice 5.3b`; name the rule, for example "the single idempotent resume path", or describe the tested behavior. Never quote private design text verbatim. `make check` rejects such citations and numbered references in tracked files.

Do not infer a new product or Runtime contract from a UI mock, fixture, example, or incidental implementation detail. When current requirements replace an older scope, update the scope explicitly and preserve unaffected safety rules. If current authoritative inputs conflict or a mechanism is undefined, report the specific gap before changing behavior.

For documentation-only work, verify links, examples, commands, scope and technical meaning. Do not run live paid models or destructive demo cleanup merely to check prose. State which behavior was inspected and which checks actually ran; documentation edits do not establish feature acceptance.

## Working method

Before editing:

1. Inspect the working tree and preserve unrelated user changes.
2. Read the authoritative material and acceptance tests relevant to the change.
3. Identify the invariant, state transition, transaction, or public contract affected.
4. Locate the existing implementation and tests before introducing a new abstraction.
5. Write or update a failing test first for behavioral changes and bug fixes.

Before a new capability starts, write a reviewable task contract in the implementation plan or task description. State its purpose and exclusions, authoritative contracts, affected packages and interfaces, persisted facts and state transitions, transaction boundaries, post-COMMIT work, recovery and concurrency behavior, required failure tests, and completion commands. Mark items that do not apply explicitly. A phase is not a single coding task: split facts, approval, feedback, multiple Actions, polling, and resolution into independently reviewable changes with explicit dependencies. Do not give an agent an entire proposal and let it fill missing contracts while coding.

Do not make a feature pass by weakening dependency allowlists, adding lint exemptions, deleting or skipping failing tests, raising file limits, rewriting old migrations or their checksum lines, or adding or raising file-size allowances. Fix the implementation or report a contract gap. When an accepted contract genuinely requires a check or test to change, identify the rule being changed, its reason, impact, and replacement evidence for separate review. The feature task alone does not authorize weakening a guard. Select the comparison commit before editing; never move it forward merely to hide a failing baseline check.

Package checks prove import direction, not behavior. Review permitted imports for their permitted use, and prove transaction, external-call, recovery, and retry boundaries with failure tests. A passing architecture test or `make check` does not by itself prove architectural correctness.

During implementation, keep the change as small as the contract allows. Do not perform opportunistic refactors, dependency upgrades, formatting sweeps, or directory reorganizations. Do not create empty packages for future work. The preparatory file split described under Code quality is the one exception, and only for a file the change is about to modify.

If the design does not answer a question that affects persisted facts, state transitions, transaction boundaries, recovery guarantees, extension compatibility, or a public API, stop and report the gap. Do not silently choose a new architecture.

## Delivery boundary

The asynchronous and long-running execution foundation is complete. Current development deepens two directions with three workloads: AI content creation (the image-generation demo and the video-editing Agent), and an Agent that repairs a dependency vulnerability, verifies the patch, obtains approval and confirms deployment. Completion of the foundation does not imply that any workload's full target is implemented or accepted.

When internal documents are available, product direction comes from `docs/private/00-vision.md`; current workload requirements come from `docs/private/requirements/req-2026-11-01-aigc-demo.md`, `docs/private/requirements/req-2026-11-02-vuln-fix-release-agent.md` and `docs/private/requirements/req-2026-11-07-video-editing-agent.md`. `docs/private/02-scope.md` consolidates their delivery boundaries. Read the relevant current contracts before coding. Earlier plans and prototypes are supporting material, not competing authority. A requirement defines a target; it does not fill missing technical contracts.

Use this rule for every proposed change:

> A change must strengthen or validate execution, or supply the minimum user path required to accept an explicitly scoped workload.

The video-editing workload brings these targets into the current scope, as a maintainer-decided technical proposal with task contracts, not as implemented behavior: an external media service for transcription, rendering and technical checks; a shared task protocol and registration for external Tools; external material references read by range and recorded in the Context Manifest; a render step that only accepts a checked timeline; per-fact-type revision limits returned to the Agent as a definite refusal; a clip-delivery completion check; and evaluation records, error analysis and judge calibration that run outside the Runtime through the public API. Implement them only through their task contracts and failure tests; do not infer a state name, fact type, endpoint or record format from this paragraph.

The vulnerability-repair workload includes Action-level authorization and approval, authenticated human facts, recoverable human waits with separate time accounting, workspace and patch facts, a Context Manifest, goal-based completion checks, definite refusals returned to the Agent, deployment reconciliation and target occupancy, and durable post-task reconciliation. These are accepted requirements with a maintainer-decided technical proposal and applicable task contracts; design approval alone does not make them existing APIs or state machines. Before implementation, read those contracts, confirm each task covers persisted facts, transitions, transactions and recovery, and resolve specific remaining gaps. Do not infer a state name, approval endpoint, retry protocol or human identity model from this paragraph.

Cancellation, learning, dynamic plugins, distributed workers, multi-tenancy, RBAC, SDKs, independent worker services, sub-Agent delegation, real video generation, an evaluation service inside the Runtime, an evaluation workbench and automatic release gates remain outside current delivery. Evaluation records, error analysis and judge calibration are in scope only as scripts outside the Runtime that call the public API; they do not add Runtime tables, endpoints or navigation. An external task gateway, sandbox runner or mock production service is a fixture or integration boundary, not a second Runtime or Worker service. A requirement to query an uncertain operation does not authorize resending it before an authoritative result. A human stop ends a wait; it is not Run cancellation, and it never decides whether an external operation took effect.

A fixed in-flight slot limit per Provider is inside the current scope: it strengthens the execution layer by proving that slot accounting neither leaks nor over-admits across crashes, retries, and uncertain results. Priority, tenant fairness, monetary budgets, and scheduling across Providers remain excluded. AIGC traffic splitting is performed by demo scripts; the Runtime owns immutable version binding, not a release-channel product.

## Package boundaries

Preserve the repository's established package layout and the dependency direction below.

- `api` parses transport input, calls `service`, and maps results. Its only direct non-Service dependencies are `runtime.ValidationError` for DTO mapping and `config/readiness` for health probes. It must not invoke Runtime decisions, query repositories, or advance executions directly.
- `service` owns use cases, Unit of Work orchestration, and scheduling of post-COMMIT work.
- `runtime` contains pure execution decisions: compilation, scheduling, aggregation, and Agent semantics. It does not access PostgreSQL, HTTP, Provider SDKs, or process-global queues.
- `store` persists facts and implements conditional updates. It does not call Providers, publish SSE, or decide the next Runtime step.
- `work` and `reconciler` call the same `service` use cases. `reconciler` may use `store` only to discover persisted work and perform Pending Callback TTL retention; it must not claim work, mutate Execution business state, write Events, or maintain a second execution path.
- `registry` owns stable Node Type, Model ID, and Tool Name resolution.
- `nodes`, `tools`, and `adapters` perform one registered operation. They do not own retry, timeout, callback routing, state transitions, or Event writes.
- `api` builds read-only Trace response projections. Projection filtering must never mutate or truncate authoritative Execution State.
- `domain` contains shared domain types and errors only. Transport DTOs, SQL rows, Provider SDK types, and UI projections do not belong there.
- `services/media/` is an external media service: a fixture-like integration boundary reached only through registered Nodes, Tools and Adapters, not a second Runtime or Worker; `evals/` holds out-of-Runtime scripts that call only the public API and import no Backend package.

Prefer explicit dependencies and constructors. Do not add a dependency injection framework, ORM, internal message bus, or Redis to solve an MVP problem already covered by the selected architecture.

## Runtime safety rules

These rules are non-negotiable:

1. PostgreSQL is the authority for Emberling-owned execution facts.
2. Run status is derived from NodeRun state. Run success is `COMPLETED`; NodeRun and Action success use `SUCCEEDED`.
3. A state transition and its corresponding Event commit in the same database transaction.
4. Node execution, Model calls, Tool calls, external Provider calls, downstream advancement, and SSE publication happen only after the prerequisite transaction commits.
5. Callback and Provider polling enter the same idempotent `resume` use case.
6. Persisted `READY` NodeRuns, Agent Turns, and Agent Actions are recoverable work. The in-process queue is only a latency optimization.
7. Conditional updates decide the single winner when immediate advancement, callback handling, timeout, polling, and reconciliation race.
8. A Run remains bound to one immutable Definition version.

Never hold a Run row lock while invoking external code. Never continue execution from uncommitted in-memory state. Never make an in-process notification, queue item, or SSE connection the recovery source.

Emberling does not promise a universal `exactly-once` or `at-least-once` guarantee for external calls. Apply the registered `SideEffectPolicy`; do not retry an uncertain external side effect merely because an Attempt is still `RUNNING`.

## Persistence and transactions

Use `pgx` and explicit SQL. Transaction ownership belongs in `service` through the Unit of Work; repositories receive the active transaction rather than opening hidden nested transactions.

- Check affected-row counts for claims and conditional state changes.
- Enforce identity and deduplication guarantees with database constraints, not process-local checks alone.
- Preserve old Attempts; a retry creates a new Attempt.
- Allocate Event sequence numbers under the Run aggregate lock.
- Store recovery-critical values in Execution State or immutable Artifact references. Event payloads remain bounded summaries.
- Keep Provider credentials, authorization headers, callback tokens, signing secrets, and internal storage keys out of business fields, Events, Trace, logs, and errors.

Every schema change includes its migration, Store mapping, relevant projection updates, and PostgreSQL integration tests. Do not rewrite a migration that has already been shared; add a forward migration. `make check` verifies every migration against `backend/migrations/checksums.sha256`; register a new migration with `make migrations-checksum`, which only appends. Both checks compare protected history with `EMBERLING_CHECK_BASE` as well as checking current checksums; local runs default to `HEAD`, so use the agreed base commit to check changes already committed on a branch.

## Agent waits and deadlines

Waiting on a person does not consume an Agent's execution time. While an Agent Run is paused for an approval, a release the Runtime reconciles, or a human wait, its deadline is not compared with the clock and timeout scans skip it. Resuming moves the deadline later by the paused duration, in the same transaction that clears the pause and appends its Event. A deadline only ever moves later, never earlier: derive elapsed time from the recorded start and pause facts, never by working backwards from the deadline. Each wait is bounded by its own persisted deadline, not by the Agent deadline.

## Extensions and external calls

Resolve Node, Model, and Tool implementations through their stable Registry contracts. Runtime behavior must come from registered metadata, not Go type assertions or inspection of execution results.

An Adapter normalizes one Provider interaction. It must not retry autonomously, write Runtime state, emit Events, or select a replacement implementation. A missing or incompatible registration fails explicitly and retains Trace; never substitute the “closest” extension or regenerate a committed Agent Decision.

Mock external systems through the deterministic Mock Provider for integration and failure tests. Unit and integration tests must not depend on live third-party credentials.

## API, SSE, and Studio

Keep transport DTOs, backend contract tests, and handwritten Studio TypeScript types aligned. Update all three in the same API change.

PostgreSQL Event rows are the authority for SSE. In-process notification only wakes a cursor query. Preserve ordered `seq` replay, deduplication, reconnect behavior, and both Snapshot-to-SSE handoff windows.

Studio renders Snapshot and SSE facts. It may validate Definition input for responsiveness, but the Backend remains the final authority. Do not reproduce Run aggregation, transition rules, retry decisions, or execution advancement in React or Zustand.

## Testing standard

Tests are part of the Runtime contract, not post-implementation cleanup.

- Unit tests cover pure compilation, validation, aggregation, and policy decisions.
- PostgreSQL integration tests cover transactions, locks, constraints, retries, recovery, callbacks, and Event ordering.
- Contract tests cover REST, callback authentication, Snapshot, Trace, and SSE behavior.
- End-to-end tests cover the accepted delivery scenarios, including scoped workload requirements as they are implemented.

Concurrency and failure tests use explicit barriers or injected hooks. Do not use arbitrary sleeps to manufacture races. Inject clocks and ID generators where outcomes depend on time or identity.

Test the failure path that justifies the code: crash after COMMIT, duplicate delivery, stale Attempt, missing registration, timeout race, rollback on Event failure, or rediscovery after restart. A mock-only repository test is not sufficient evidence for a PostgreSQL transaction or locking guarantee.

Run the narrowest relevant test while iterating, then run the repository gate before completion. `make check-fast` covers the static guards and unit tests while iterating and does not replace the gate:

```sh
make check
```

`make check` runs golangci-lint with `backend/.golangci.yml` (installed by `make bootstrap`) and runs Go tests with the race detector. A `//nolint` directive must name the specific linter and give a reason.

If the full gate cannot run, report the exact command, failure, and unverified surface. Never claim a check passed when it was skipped or unavailable.

## Code quality

Write straightforward, idiomatic Go and TypeScript. Optimize for correctness and maintainability before abstraction.

- Keep functions focused on one transaction or one pure decision.
- Keep each source file to one use case or one pure-decision topic, following the existing split in `service/agent_turn.go`, `agent_final.go` and `agent_timeout.go`. Treat roughly 800 non-test lines as a signal to check whether new code belongs there. Do not add a new use case to a file over 1,200 lines: first split it by use case in a separate commit that only moves code within the same package, changes no behavior or transaction boundary, and passes `make check`. `make check` enforces the 1,200-line limit; files already over it are listed in `scripts/file-size-baseline.txt`, which may only shrink. Never split into `helpers.go`, `util.go` or `common.go`, and never use a split to introduce new abstractions.
- Use domain-specific names; avoid generic `Manager`, `Helper`, `Util`, and `Common` packages.
- Wrap errors with operation and stable identifiers, while excluding secrets and oversized payloads.
- Propagate `context.Context` through I/O boundaries and honor cancellation of the request or worker, without inventing Run cancellation semantics.
- Bound queues, reads, payloads, retries, and polling loops.
- Make ownership of goroutines explicit and stop them during shutdown.
- Avoid package globals for mutable Runtime state.
- Add dependencies only when the standard library or selected baseline cannot meet the requirement clearly.
- Keep generated files reproducible and do not edit generated output by hand.

Comments explain invariants, ownership, concurrency, or non-obvious tradeoffs. Do not narrate code that is already clear from names and structure.

## Public documentation

Long-form public documents and README assets live under `docs/public/`; internal documents live under the git-ignored `docs/private/`.

`README.md` and `README.zh-CN.md` are public product landing pages. Keep them self-contained and synchronized. Do not link to private files, expose internal planning material, or turn the README into an implementation specification.

Do not change product scope or architecture as a side effect of implementation. If a code change requires a new public guarantee, obtain explicit approval and update the relevant public explanation in the same change.

## Completion checklist

Before handing off a change, confirm that:

- The implementation matches the accepted contract and remains inside the accepted delivery scope.
- Package dependencies follow the documented direction.
- State, Event, and transaction behavior remain atomic.
- All external work occurs after COMMIT.
- Recovery does not depend on process memory.
- Schema and API changes include all required mappings, migrations, and tests.
- New behavior has a failure-focused test and is covered by the relevant acceptance or contract test.
- Sensitive values do not appear in logs, Events, Trace, API responses, or fixtures.
- Formatting, focused tests, and `make check` have been run, or limitations are reported precisely.
- The final diff contains no unrelated edits, debug code, temporary files, or accidental generated output.
