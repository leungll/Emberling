import { between, failFocusId, type RunModel, type Step } from './run-model';
import type { AgentTurnTrace, ToolAttemptTrace, ToolMetadata } from '@/api/types';
import { copy } from '@/lib/copy';
import { duration, limitText } from '@/lib/time';
import { carryFrom } from '@/lib/nav/list-state';

/**
 * What happened, why and what to do next, from returned fields only: Studio wording per
 * stable error code. The raw message stays in Technical details.
 */
export interface Failure {
  code: string | null;
  step: Step;
  turn: AgentTurnTrace | null;
  tool: string | null;
  title: string;
  cause: string;
  /** "Deployment Result did not run." */
  effect: string;
  next: string;
  /** The Editor field the fix lives in, or null for the step itself. */
  field: string | null;
  nextLabel: string;
  /** The failed tool changes an outside system and is never re-sent on its own. */
  notRetriedTool: string | null;
  /** The element where the failure starts. */
  focus: string;
}

function numberText(v: unknown): string {
  return typeof v === 'number' ? String(v) : '';
}

function configNumber(config: Record<string, unknown>, key: string): number | null {
  const v = config[key];
  return typeof v === 'number' ? v : null;
}

/**
 * A failed Agent tool call is not retried automatically when the tool changes an outside
 * system: its side-effect policy, not this page, decides that.
 */
export function notRetried(
  tool: string | null,
  attempts: readonly ToolAttemptTrace[],
  tools: readonly ToolMetadata[],
): boolean {
  const meta = tool ? tools.find((x) => x.name === tool) : undefined;
  const last = attempts[attempts.length - 1];
  return meta?.sideEffect.kind === 'EXTERNAL' && last?.status === 'FAILED';
}

export function failureOf(model: RunModel, tools: readonly ToolMetadata[]): Failure | null {
  const fs = model.failedStep;
  if (!fs?.nodeRun) return null;
  const d = copy.runs.diagnosis;
  const err = fs.nodeRun.error ?? model.snapshot.run.error;
  const details = (err?.details ?? {}) as Record<string, unknown>;
  const name = fs.node.name;
  const turn = fs.trace?.turns.find((x) => x.action?.status === 'FAILED') ?? null;
  const tool = turn?.decision?.tool ?? null;
  const attempts = fs.detail?.attempts ?? [];
  const config = fs.node.config as Record<string, unknown>;
  const code = err?.code ?? null;

  let title: string;
  let cause: string = d.undescribed;
  let next = '';
  let field: string | null = null;
  let nextLabel: string = d.openStep;

  switch (code) {
    case 'EXECUTOR_ERROR': {
      title = d.serviceFailed(name);
      const first = attempts[0];
      const hitLimit =
        attempts.length > 0 &&
        attempts.every(
          (a) =>
            a.deadlineAt &&
            a.completedAt &&
            new Date(a.completedAt).getTime() >= new Date(a.deadlineAt).getTime(),
        );
      const limit = first?.deadlineAt ? duration(between(first.startedAt, first.deadlineAt)) : '';
      cause = hitLimit
        ? d.eachAttemptLimit(attempts.length, limit)
        : d.allAttemptsFailed(attempts.length);
      next = d.checkService;
      break;
    }
    case 'TIMEOUT':
      title = d.ranOutOfTime(name);
      cause = d.timeLimitIn(
        limitText(configNumber(config, 'timeoutMs')),
        turn?.turnNo ?? null,
        tool,
      );
      field = 'timeoutMs';
      nextLabel = d.changeTimeLimit;
      break;
    case 'PRECONDITION_UNMET': {
      title = d.blockedFrom(name, tool);
      const factType = typeof details.factType === 'string' ? details.factType : '';
      const subject = details.subject;
      let noun: string = d.nounItem;
      for (const fact of fs.trace?.facts.items ?? []) {
        if (fact.subject !== subject) continue;
        const artifact = fact.bindings.artifact as { mediaType?: unknown } | undefined;
        if (typeof artifact?.mediaType === 'string' && artifact.mediaType.startsWith('image/')) {
          noun = d.nounImage;
        }
      }
      const label = copy.factLabel[factType] ?? factType.replace(/_/g, ' ');
      cause = d.needsFirst(tool, noun, d.factGate[factType] ?? d.passing(label));
      field = 'instructions';
      nextLabel = d.reviewInstructions;
      break;
    }
    case 'GENERATION_LIMIT_REACHED':
      title = d.limitReached(name);
      cause = d.usedOf(numberText(details.used), numberText(details.limit), tool);
      field = 'maxGenerationCalls';
      nextLabel = d.changeGenerationLimit;
      break;
    default: {
      const entry = code
        ? (
            copy.errorCause as unknown as Record<
              string,
              ((c: Record<string, unknown>) => string) | undefined
            >
          )[code]
        : undefined;
      title = d.stepFailed(name);
      if (entry) {
        cause = entry({
          step: name,
          tool: tool ?? undefined,
          limit: code === 'MAX_TURNS' ? (configNumber(config, 'maxTurns') ?? undefined) : undefined,
        });
      }
      const nextWords = code
        ? (copy.errors as Record<string, { next: string } | undefined>)[code]
        : undefined;
      if (nextWords && code !== 'EXECUTOR_ERROR') {
        nextLabel = nextWords.next;
        field = FIELD_OF[code ?? ''] ?? null;
      }
    }
  }

  const notRun =
    model.snapshot.run.status === 'FAILED'
      ? model.steps.filter((s) => !s.nodeRun).map((s) => s.node.name)
      : [];
  return {
    code,
    step: fs,
    turn,
    tool,
    title,
    cause,
    effect: notRun.length ? d.effect(notRun) : '',
    next,
    field,
    nextLabel,
    notRetriedTool: turn && notRetried(tool, turn.toolAttempts, tools) ? tool : null,
    focus: failFocusId(model) ?? fs.node.id,
  };
}

/**
 * The Editor field each stable error code's fix lives in, named by the step's config key as
 * the Editor's `?field=` reads it.
 */
const FIELD_OF: Record<string, string> = {
  TIMEOUT: 'timeoutMs',
  MAX_TURNS: 'maxTurns',
  PRECONDITION_UNMET: 'instructions',
  GENERATION_LIMIT_REACHED: 'maxGenerationCalls',
  FACT_BASIS_MISSING: 'instructions',
  MODEL_ERROR: 'modelId',
  TOOL_ERROR: 'instructions',
  INVALID_ACTION: 'outputSchema',
};

/**
 * The Editor address for a step and field of the run's workflow. `search` is the run view's
 * own address, so the Editor keeps going back to the list the run was opened from.
 */
export function editHref(
  workflowId: string,
  nodeId: string | null,
  field: string | null,
  search = '',
): string {
  const q = new URLSearchParams();
  if (nodeId) q.set('select', nodeId);
  if (field) q.set('field', field);
  const s = q.toString();
  return carryFrom(`/workflows/${encodeURIComponent(workflowId)}${s ? `?${s}` : ''}`, search);
}
