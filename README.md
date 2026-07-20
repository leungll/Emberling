# Emberling

A visual workflow engine for AI applications, built around durable async execution and first-class observability.

**Status:** Design phase, implementation not started · **Stack:** React, TypeScript, React Flow, Go, PostgreSQL

[中文](./README.zh-CN.md)

## Overview

Emberling lets you build AI workflows as a directed graph — LLM calls, prompts, tools, conditions, and long-running external tasks — then run, inspect, and evaluate them. Workflows are authored on a canvas and compiled to a DSL that the runtime executes independently of how it was produced (UI, JSON import, or API).

The project's focus is the execution and observability layer, not the editor. Calling a model is easy; running a multi-step AI pipeline reliably — with suspend/resume, retries, durable state, and a complete execution trace — is where most of the engineering effort goes.

## Design goals

- **Durable async execution.** Nodes can dispatch a long-running external task, suspend the run to persistent storage, and resume from the checkpoint when the task calls back. A run in flight survives a backend restart; a reconciler backstops missed callbacks.
- **Observability by default.** Every run emits an immutable event sequence, streamed over SSE. Node state, inputs, outputs, latency, token usage, and errors are recorded as the run executes, not reconstructed afterward.
- **Extensible node model.** New node types register against a common interface; adding one does not require changes to the runtime core.
- **Design-time / run-time separation.** The editor produces a versioned workflow DSL; the runtime consumes it. The two evolve independently through that contract.
- **Evaluation as a first-class concern** *(roadmap).* Datasets, batch runs, evaluators (including LLM-as-judge), comparison reports, and regression detection, built on the same execution engine and event trace.

## Execution model

The runtime compiles a workflow into a DAG and executes it topologically. Nodes run in one of two modes:

- **Synchronous** — the node computes a result and returns (prompt rendering, LLM calls, transforms).
- **Asynchronous** — the node dispatches work to an external system and returns immediately. The run's state is persisted and the run enters `PAUSED` / `WAITING_CALLBACK`. When the external system reports completion via an idempotent callback endpoint, the run is rebuilt from persisted state and downstream scheduling resumes.

Callback handling is idempotent (a given external task advances a run at most once), and durability is verified by restarting the backend mid-run and confirming the suspended run still completes.

## Example workflows

The same engine drives structurally different flows:

| Workflow | Exercises |
|---|---|
| Document processing | Chained synchronous LLM nodes, context passing, trace |
| Research | Tool nodes, external API calls, error handling |
| Content review | Conditions, human-in-the-loop, pause/resume |
| AIGC media generation | Async dispatch, suspend/resume, durability, mixed media/text pipeline |

AIGC media generation is the primary MVP scenario: rewrite a user prompt into a render prompt (sync) → dispatch an image render and suspend (async) → resume on callback → generate a caption (sync) → output. The async node can target any image-generation API, or a mock render service for closing the suspend/resume loop end to end.

## Roadmap

| Phase | Scope |
|---|---|
| 1 — Durable foundation | End-to-end MVP that proves durable async execution: editor, DAG validation, sequential execution, retries/timeouts, async suspend/resume, SSE trace, PostgreSQL persistence |
| 2 — Runtime maturity | HTTP/condition nodes, parallel execution, cancellation, provider abstraction, streaming output, cost/token tracking, versioning |
| 3 — Extensibility | Node registry, dynamic node metadata, node SDK, import/export DSL, MCP client, hosted demo |
| 4 — Evaluation & memory | Session memory, datasets, batch runs, evaluators, comparison and regression reports |
| 5 — Distributed execution | Worker pool, queue-based scheduling, distributed locking, human-approval state, deployment API, OpenTelemetry |

## Status

This repository currently contains the project design. No implementation has started. This README describes the intended system; code and demos will follow.
