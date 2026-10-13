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
import { nodeIcon } from './node-icons';
import { eventsFor } from './run-events';
import type { TraceElement } from './run-model';
import { copy as words } from '@/lib/copy';

/** The details pane's tabs, kind words and path labels per element. */

export type DetailTab = 'overview' | 'turns' | 'input' | 'output' | 'facts' | 'events' | 'raw';

interface TabDef {
  value: DetailTab;
  label: string;
  count?: number;
}

export function tabsFor(el: TraceElement, ctx: DetailContext): TabDef[] {
  const t = words.runs.details.tabs;
  const overview: TabDef = { value: 'overview', label: t.overview };
  const raw: TabDef = { value: 'raw', label: t.raw };
  switch (el.kind) {
    case 'run':
      return [
        overview,
        { value: 'input', label: t.input },
        { value: 'output', label: t.output },
        raw,
      ];
    case 'step': {
      const step = el.step;
      if (!step?.nodeRun) return [overview, raw];
      const tabs: TabDef[] = [overview];
      if (step.trace) tabs.push({ value: 'turns', label: t.turns, count: step.trace.turns.length });
      tabs.push({ value: 'input', label: t.input }, { value: 'output', label: t.output });
      const facts = step.trace?.facts.items.length ?? 0;
      if (facts) tabs.push({ value: 'facts', label: t.facts, count: facts });
      const events = eventsFor(ctx.model, el).length;
      if (events) tabs.push({ value: 'events', label: t.events, count: events });
      tabs.push(raw);
      return tabs;
    }
    case 'attempt':
      return el.nodeAttempt
        ? [overview, { value: 'input', label: t.input }, { value: 'output', label: t.result }, raw]
        : [overview, raw];
    case 'action':
      return el.turn?.decision?.kind === 'FINAL'
        ? [overview, { value: 'output', label: t.output }, raw]
        : [
            overview,
            { value: 'input', label: t.arguments },
            { value: 'output', label: t.result },
            raw,
          ];
    case 'group':
      return [overview];
    default:
      return [overview, raw];
  }
}

export function kindOf(el: TraceElement): { icon: LucideIcon; word: string } {
  const k = words.runs.details.kind;
  switch (el.kind) {
    case 'run':
      return { icon: Activity, word: k.run };
    case 'step':
      return { icon: nodeIcon(el.step?.node.type ?? ''), word: k.step(el.step?.typeLabel ?? '') };
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
