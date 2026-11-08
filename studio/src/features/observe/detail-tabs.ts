import {
  Activity,
  ArrowRightFromLine,
  Bot,
  Hourglass,
  RotateCw,
  Sparkle,
  Wrench,
  type LucideIcon,
} from 'lucide-react';

import type { DetailContext } from './detail-context';
import { stepIcon } from '@/components/step/step-icons';
import type { TraceElement } from './run-model';
import { copy as words } from '@/lib/copy';

/** The details pane's tabs, kind words and path labels per element. */

export type DetailTab =
  'overview' | 'turns' | 'data' | 'input' | 'output' | 'facts' | 'events' | 'raw';

interface TabDef {
  value: DetailTab;
  label: string;
  count?: number;
}

/**
 * At most three tabs. Overview reads the element; Turns lists an Agent's turns;
 * Data holds its input, output, saved results and raw records.
 */
export function tabsFor(el: TraceElement, _ctx: DetailContext): TabDef[] {
  const t = words.runs.details.tabs;
  const overview: TabDef = { value: 'overview', label: t.overview };
  const data: TabDef = { value: 'data', label: t.data };
  if (el.kind === 'group') return [overview];
  if (el.kind === 'step' && el.step?.trace)
    return [overview, { value: 'turns', label: t.turns, count: el.step.trace.turns.length }, data];
  return [overview, data];
}

export function kindOf(el: TraceElement): { icon: LucideIcon; word: string } {
  const k = words.runs.details.kind;
  switch (el.kind) {
    case 'run':
      return { icon: Activity, word: k.run };
    case 'step':
      return { icon: stepIcon(el.step?.node.type ?? ''), word: k.step(el.step?.typeLabel ?? '') };
    case 'attempt':
      return { icon: RotateCw, word: el.nodeAttempt ? k.attempt : k.toolAttempt };
    case 'turn':
      return { icon: Bot, word: k.turn };
    case 'group':
      return { icon: Bot, word: k.group };
    case 'decision':
      return { icon: Sparkle, word: k.decision };
    case 'action':
      return el.turn?.decision?.kind === 'FINAL'
        ? { icon: ArrowRightFromLine, word: k.finalAnswer }
        : { icon: Wrench, word: k.toolCall };
    case 'wait':
      return { icon: Hourglass, word: k.wait };
  }
}

export function crumbLabel(el: TraceElement): string {
  const c = words.runs.details.crumb;
  switch (el.kind) {
    case 'run':
      return c.run;
    case 'group': {
      const turns = el.turns ?? [];
      return c.turns(turns[0]?.turnNo ?? 0, turns[turns.length - 1]?.turnNo ?? 0);
    }
    case 'turn':
      return c.turn(el.turn?.turnNo ?? 0);
    case 'decision':
      return c.decision;
    case 'attempt':
      return c.attempt(el.nodeAttempt?.attemptNo ?? el.toolAttempt?.attemptNo ?? 0);
    case 'wait':
      return c.wait;
    default:
      return el.label;
  }
}
