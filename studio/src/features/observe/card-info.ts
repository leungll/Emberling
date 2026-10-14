import type {
  AgentTurnTrace,
  ExecutionFactTrace,
  JsonValue,
  ToolAttemptTrace,
  ToolDeploymentTrace,
  ToolPhotoTrace,
  ToolTestTrace,
  WithheldToolField,
} from '@/api/types';
import {
  collectMedia,
  mediaFormat,
  mediaWord,
  parseJsonText,
  type MediaRef,
} from '@/components/media/media-ref';
import { copy } from '@/lib/copy';
import { factImage, imagesIn, photoList, photoNo, reviewOf } from './run-media';
import type { RunModel, Step, TraceElement } from './run-model';

/**
 * What a run graph card says under the step's name: what the step is doing now or what it
 * produced, the Agent's turn, and its images. Built only from returned records: the
 * NodeRun (status, error, input and output), its Agent Trace and its Attempts. A number
 * shown is a field of one record (a limit, a turn number, passedCount, a photo's position,
 * a test summary's total and failed) or the length of one returned list (the photo set's
 * photos, the images the Agent answered with); Studio never counts records across the run
 * and never derives one number from others.
 */

/** A sentence with the tool names in it, for Words. */
export interface CardLine {
  text: string;
  tools: string[];
}

export interface CardInfo {
  line: CardLine;
  /** A text preview: clamped to two lines, `full` holds all of it. */
  clamp: boolean;
  full: string | null;
  /** Nothing ran or nothing came out: the line is muted. */
  idle: boolean;
  /** "Turn 3" while an Agent works, "Finished in turn 9" once it answered. */
  turn: string | null;
  refs: MediaRef[];
  /** Names the thumbnails when they are not the step's result. */
  refsLabel: string | null;
}

const TEXT_TYPES: readonly string[] = [
  'text_input',
  'text_output',
  'text_generation',
  'prompt_template',
];
const IMAGE_TYPES: readonly string[] = [
  'image_input',
  'image_generation',
  'media_brief',
  'media_result',
  'media_output',
];

type Obj = Record<string, unknown>;
const isObj = (v: unknown): v is Obj => typeof v === 'object' && v !== null && !Array.isArray(v);

const c = () => copy.runs.card;
const line = (text: string, tools: string[] = []): CardLine => ({ text, tools });

function errorShort(code: string | undefined): string {
  const entry = code
    ? (copy.errors as Record<string, { short: string } | undefined>)[code]
    : undefined;
  return entry?.short ?? c().failed;
}

function outputText(output: JsonValue | null): string | null {
  return isObj(output) && typeof output.text === 'string' ? output.text : null;
}

/** The images every saved result of the Agent describes, in the order saved. */
function savedRefs(step: Step): MediaRef[] {
  return (step.trace?.facts.items ?? []).flatMap((f) => {
    const r = factImage(f);
    return r ? [r] : [];
  });
}

const targetOf = (d: ToolDeploymentTrace | undefined): string | null =>
  d?.target ? c().target(d.target.service, d.target.environment) : null;

/** "Tests passed (12)", "Tests failed (3 of 12)": the runner's two recorded numbers. */
function testLine(t: ToolTestTrace): CardLine {
  const { total, failed } = t.summary;
  if (!t.passed) return line(c().testsFailedOf(failed, total));
  return line(failed ? c().testsPassedWithFailures(failed, total) : c().testsPassedOf(total));
}

function deployedLine(d: ToolDeploymentTrace | undefined): CardLine | null {
  const target = targetOf(d);
  const version = d?.version;
  if (version) return line(target ? c().deployed(version, target) : c().deployedVersion(version));
  return target ? line(c().deployedTo(target)) : null;
}

const VIDEO_URL = 'video.videoUrl';

function withheld(attempt: ToolAttemptTrace, field: string): WithheldToolField | undefined {
  return attempt.withheld?.find((w) => w.field === field);
}

/** What one finished tool call returned: the results it saved, else its projected result. */
export function callResult(model: RunModel, step: Step, turn: AgentTurnTrace): CardLine | null {
  const attempt = turn.toolAttempts[turn.toolAttempts.length - 1];
  if (!attempt) return null;
  const facts = (step.trace?.facts.items ?? []).filter((f) => f.toolAttemptId === attempt.id);
  const image = facts.find((f) => f.factType === 'image_generated');
  if (image) {
    const n = photoNo(model, image.bindings.photoAssetId);
    return line(n !== null ? c().generatedImageFor(n) : c().generatedImage);
  }
  const review = facts.find((f) => f.factType === 'asset_reviewed');
  if (review) {
    const verdict = review.verdict ? c().passedReview : c().didNotPassReview;
    const n = photoNo(model, review.bindings.photoAssetId);
    return line(n !== null ? c().ofPhoto(verdict, n) : verdict);
  }
  if (attempt.test) return testLine(attempt.test);
  const tested = facts.find((f) => f.factType === 'patch_tested');
  if (tested) return line(tested.verdict ? c().testsPassed : c().testsFailed);
  const deployed = deployedLine(attempt.deployment);
  if (deployed) return deployed;
  if (attempt.video) return line(c().videoReady);
  if (withheld(attempt, VIDEO_URL)) return line(c().videoReadyLinkWithheld);
  return null;
}

/** Tools whose saved image or review a Turns row already lists under the call. */
const SAVES_ITS_RESULT: readonly string[] = ['generate_image', 'review_asset'];

/** A Turns row's "Result:" line: a finished call's result its saved results do not show. */
export function turnResult(model: RunModel, step: Step, turn: AgentTurnTrace): CardLine | null {
  const tool = turn.decision?.tool;
  if (turn.decision?.kind !== 'TOOL_CALL' || !tool || SAVES_ITS_RESULT.includes(tool)) return null;
  return turn.action?.status === 'SUCCEEDED' ? callResult(model, step, turn) : null;
}

export interface CallSaved {
  facts: ExecutionFactTrace[];
  result: CardLine | null;
  /** The video the call made, when the trace returns its link. */
  videoUrl: string | null;
  /** Result members the trace left out, and why. */
  withheld: WithheldToolField[];
}

/** What a tool call saved and returned; null when the trace holds neither. */
export function callSaved(model: RunModel, el: TraceElement): CallSaved | null {
  const turn = el.turn;
  const step = el.step;
  if (!turn || !step?.trace || turn.decision?.kind !== 'TOOL_CALL') return null;
  const ids = new Set(turn.toolAttempts.map((a) => a.id));
  const facts = step.trace.facts.items.filter((f) => ids.has(f.toolAttemptId));
  const done = turn.action?.status === 'SUCCEEDED';
  const result = done ? callResult(model, step, turn) : null;
  const attempt = turn.toolAttempts.at(-1);
  const videoUrl = done ? (attempt?.video?.videoUrl ?? null) : null;
  const left = done && attempt ? (attempt.withheld ?? []) : [];
  return facts.length || result || left.length ? { facts, result, videoUrl, withheld: left } : null;
}

export interface CallArguments {
  photo: ToolPhotoTrace | null;
  /** "checkout in staging". */
  target: string | null;
  withheld: WithheldToolField[];
}

/** The arguments the trace projects for a tool call; null when it projects none. */
export function callArguments(el: TraceElement): CallArguments | null {
  const turn = el.turn;
  if (!turn || turn.decision?.kind !== 'TOOL_CALL') return null;
  const attempt = el.toolAttempt ?? turn.toolAttempts.at(-1);
  if (!attempt) return null;
  const photo = attempt.photo ?? null;
  const target = targetOf(attempt.deployment);
  const left = (attempt.withheld ?? []).filter((w) => w.field !== VIDEO_URL);
  return photo || target || left.length ? { photo, target, withheld: left } : null;
}

function failureLine(step: Step): CardLine {
  const err = step.nodeRun?.error;
  const details = isObj(err?.details) ? err.details : {};
  const failedTurn = step.trace?.turns.find((t) => t.action?.status === 'FAILED');
  const tool = failedTurn?.decision?.tool ?? null;
  switch (err?.code) {
    case 'GENERATION_LIMIT_REACHED':
      if (typeof details.limit === 'number') return line(c().usedAllCalls(details.limit));
      break;
    case 'PRECONDITION_UNMET':
      if (tool) {
        return line(
          details.factType === 'asset_reviewed'
            ? c().blockedNeedsReview(tool)
            : c().blockedRequirement(tool),
          [tool],
        );
      }
      break;
    case 'TIMEOUT':
      if (tool) return line(c().outOfTimeWhile(tool), [tool]);
      break;
  }
  return line(errorShort(err?.code));
}

function waitingLine(step: Step): CardLine {
  const turn = step.trace?.turns.find((t) => t.action?.status === 'WAITING_CALLBACK');
  const tool = turn?.decision?.tool;
  if (turn && tool) {
    const at = turn.toolAttempts.find((a) => a.status === 'DISPATCHED') ?? turn.toolAttempts.at(-1);
    return line(c().waitingToReport(copy.runs.provider(at?.callbackBinding?.providerId), tool), [
      tool,
    ]);
  }
  const attempts = step.detail?.attempts ?? [];
  const binding = attempts[attempts.length - 1]?.callbackBinding;
  return line(c().waitingToReportBack(copy.runs.provider(binding?.providerId)));
}

function agentRunningLine(step: Step): CardLine {
  const last = step.trace?.turns.at(-1);
  const decision = last?.decision;
  const action = last?.action;
  if (!last || !decision || last.status === 'READY' || last.status === 'RUNNING') {
    return line(c().deciding);
  }
  if (action && (action.status === 'RUNNING' || action.status === 'READY')) {
    if (decision.kind === 'FINAL') return line(c().passingAnswer);
    const tool = decision.tool ?? '';
    const attempt = last.toolAttempts.at(-1);
    const photo = attempt?.photo;
    if (photo?.position !== undefined && photo.count !== undefined) {
      return line(c().callingForPhoto(tool, photo.position, photo.count), [tool]);
    }
    const target = targetOf(attempt?.deployment);
    if (target) return line(c().deployingTo(target));
    return line(c().calling(tool), [tool]);
  }
  return line(c().deciding);
}

function textPreview(text: string): Pick<CardInfo, 'line' | 'clamp' | 'full'> {
  const v = parseJsonText(text);
  if (isObj(v)) return { line: line(c().jsonWith(Object.keys(v))), clamp: false, full: null };
  const flat = text.replace(/\s+/g, ' ').trim();
  return { line: line(flat), clamp: true, full: flat };
}

/** The step's own images: its output's references, or an image input's input. */
function ownImages(model: RunModel, step: Step): MediaRef[] {
  const nr = step.nodeRun;
  if (!nr) return [];
  if (step.trace) return imagesIn(model, nr.output);
  const refs = collectMedia(nr.output);
  return refs.length || step.node.type !== 'image_input' ? refs : collectMedia(nr.input);
}

export function cardInfo(model: RunModel, step: Step): CardInfo {
  const out: CardInfo = {
    line: line(''),
    clamp: false,
    full: null,
    idle: false,
    turn: null,
    refs: [],
    refsLabel: null,
  };
  const nr = step.nodeRun;
  if (!nr) {
    return {
      ...out,
      line: line(step.statusKey === 'NONE_RUN_FAILED' ? c().didNotRun : c().notStarted),
      idle: true,
    };
  }
  const trace = step.trace;
  const last = trace?.turns.at(-1);
  if (last && (nr.status === 'RUNNING' || nr.status === 'WAITING_CALLBACK')) {
    out.turn = c().turn(last.turnNo);
  }
  if (trace && nr.status === 'SUCCEEDED' && trace.agentRun.termination === 'FINAL_RESPONSE') {
    out.turn = c().finishedInTurn(trace.agentRun.currentTurnNo);
  }

  switch (nr.status) {
    case 'READY':
      return { ...out, line: line(c().queued) };
    case 'FAILED': {
      const refs = savedRefs(step);
      return {
        ...out,
        line: failureLine(step),
        refs,
        refsLabel: refs.length ? c().imagesBeforeFailure : null,
      };
    }
    case 'WAITING_CALLBACK':
      return { ...out, line: waitingLine(step) };
    case 'RUNNING': {
      if (!trace) return { ...out, line: line(c().runningAs[step.node.type] ?? c().running) };
      const refs = savedRefs(step);
      return {
        ...out,
        line: agentRunningLine(step),
        refs,
        refsLabel: refs.length ? c().imagesSoFar : null,
      };
    }
    case 'SUCCEEDED':
      break;
  }

  if (trace) {
    const refs = ownImages(model, step);
    if (refs.length) return { ...out, line: line(c().answeredWith(refs.length)), refs };
    const calls = trace.turns.filter(
      (t) => t.decision?.kind === 'TOOL_CALL' && t.action?.status === 'SUCCEEDED',
    );
    const lastCall = calls[calls.length - 1];
    const result = lastCall ? callResult(model, step, lastCall) : null;
    return { ...out, line: result ?? line(c().finishedWithAnswer) };
  }

  const type = step.node.type;
  if (TEXT_TYPES.includes(type)) {
    const text = outputText(nr.output);
    if (!text) return { ...out, line: line(c().noText), idle: true };
    return { ...out, ...textPreview(text) };
  }
  if (type === 'media_brief') {
    const photos = photoList(model);
    return { ...out, line: line(c().photos(photos.length)), refs: photos };
  }
  if (type === 'media_result') {
    const review = reviewOf(nr.output);
    const refs = collectMedia(isObj(nr.output) ? nr.output.image : null);
    return {
      ...out,
      line: line(review ? c().passedOf(review.passedCount, review.photoCount) : c().resultReady),
      refs,
    };
  }
  const refs = ownImages(model, step);
  const first = refs[0];
  if (first) {
    const word = type === 'media_output' ? c().finalImage : mediaWord(first);
    return { ...out, line: line(c().withFormat(word, mediaFormat(first))), refs };
  }
  if (IMAGE_TYPES.includes(type)) {
    return {
      ...out,
      line: line(type === 'image_input' ? c().noImageGiven : c().noImage),
      idle: true,
    };
  }
  return { ...out, line: line(c().succeeded) };
}
