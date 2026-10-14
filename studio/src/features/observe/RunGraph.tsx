import { Maximize, Minus, Plus } from 'lucide-react';
import {
  useEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
  type PointerEvent,
  type ReactNode,
} from 'react';

import { ElementTime } from './RunFacts';
import { stepIcon } from '@/components/step/step-icons';
import {
  CARD_H,
  CARD_W,
  GAP_Y,
  ZOOM_MAX,
  ZOOM_MIN,
  edgePath,
  fitView,
  graphLayout,
  graphMove,
  lodCardHeight,
  revealCard,
  zoomAt,
  zoomLevel,
  type View,
} from './run-graph-layout';
import { stepElementOf, type RunModel, type Step } from './run-model';
import { statusInfo } from '@/components/status/status-info';
import { StatusIndicator } from '@/components/status/StatusIndicator';
import { IconButton } from '@/components/ui/button';
import { Tooltip } from '@/components/ui/tooltip';
import { copy as words, useCopy } from '@/lib/copy';
import { duration } from '@/lib/time';
import { cn } from '@/lib/utils';

/**
 * The run graph: the bound version's steps, read-only, laid out like the Editor's canvas,
 * each card with the step's status. It opens fitted; drag or scroll pans, Ctrl or Cmd
 * with scroll zooms. Below the full-card zoom each card is one header row.
 */

interface RunGraphProps {
  model: RunModel;
  selectedId: string;
  onSelect: (nodeId: string) => void;
}

interface ViewState extends View {
  user: boolean;
}

/** The words under a card's name: why it failed, that it waits, or its turns and time. */
function CardMeta({ model, step }: { model: RunModel; step: Step }): ReactNode {
  const t = words.runs.trace;
  const nr = step.nodeRun;
  if (!nr) return step.statusKey === 'NONE_RUN_FAILED' ? t.didNotRun : t.notStarted;
  if (nr.status === 'FAILED') {
    const code = nr.error?.code;
    const entry = code
      ? (words.errors as Record<string, { short: string } | undefined>)[code]
      : undefined;
    return entry?.short ?? words.runs.events.summary.failed;
  }
  if (nr.status === 'WAITING_CALLBACK') return t.waitStatus.live;
  const el = model.byId.get(step.node.id);
  const turns = step.trace ? words.runs.graph.turns(step.trace.turns.length) : '';
  return (
    <>
      {turns}
      {turns && el ? ', ' : null}
      {el ? <ElementTime model={model} el={el} /> : null}
    </>
  );
}

/** Plain words of the card meta, for the card's accessible name. */
function metaText(step: Step): string {
  const t = words.runs.trace;
  const nr = step.nodeRun;
  if (!nr) return step.statusKey === 'NONE_RUN_FAILED' ? t.didNotRun : t.notStarted;
  if (nr.status === 'FAILED') {
    const code = nr.error?.code;
    const entry = code
      ? (words.errors as Record<string, { short: string } | undefined>)[code]
      : undefined;
    return entry?.short ?? words.runs.events.summary.failed;
  }
  if (nr.status === 'WAITING_CALLBACK') return t.waitStatus.live;
  const parts: string[] = [];
  if (step.trace) parts.push(words.runs.graph.turns(step.trace.turns.length));
  if (nr.latencyMs !== null) parts.push(duration(nr.latencyMs));
  return parts.join(', ');
}

export function RunGraph({ model, selectedId, onSelect }: RunGraphProps) {
  const copy = useCopy();
  const g = copy.runs.graph;
  const def = model.definition;
  const layout = useMemo(() => graphLayout(def), [def]);
  const viewport = useRef<HTMLDivElement>(null);
  const [size, setSize] = useState({ w: 0, h: 0 });
  const [view, setView] = useState<ViewState>({ z: 1, tx: 0, ty: 0, user: false });
  const [focusId, setFocusId] = useState<string | null>(null);
  const [revealed, setRevealed] = useState<string | null>(null);
  const drag = useRef<{ x: number; y: number; tx: number; ty: number } | null>(null);
  const [panning, setPanning] = useState(false);

  useEffect(() => {
    const el = viewport.current;
    if (!el) return;
    const ro = new ResizeObserver(() => {
      setSize({ w: el.clientWidth, h: el.clientHeight });
    });
    ro.observe(el);
    return () => {
      ro.disconnect();
    };
  }, []);

  const shown: View = view.user || !size.w ? view : fitView(layout, size.w, size.h);
  const level = zoomLevel(shown.z);
  const lod = level === 'full' ? 0 : lodCardHeight(shown.z);
  const heightOf = () => (lod ? lod : CARD_H);

  // Keep the selected step in view: pan the least distance that shows its card.
  const stepId = stepElementOf(model.byId.get(selectedId))?.id ?? null;
  if (stepId !== revealed && size.w) {
    setRevealed(stepId);
    const p = stepId ? layout.pos.get(stepId) : undefined;
    const next = p ? revealCard(shown, p, heightOf(), size.w, size.h) : null;
    if (next) setView({ ...next, user: true });
  }

  const latest = useRef(shown);
  useEffect(() => {
    latest.current = shown;
  });
  useEffect(() => {
    const el = viewport.current;
    if (!el) return;
    const onWheel = (e: WheelEvent) => {
      e.preventDefault();
      const v = latest.current;
      if (e.ctrlKey || e.metaKey) {
        const r = el.getBoundingClientRect();
        setView({
          ...zoomAt(v, v.z * Math.exp(-e.deltaY / 300), e.clientX - r.left, e.clientY - r.top),
          user: true,
        });
        return;
      }
      setView({ ...v, tx: v.tx - e.deltaX, ty: v.ty - e.deltaY, user: true });
    };
    el.addEventListener('wheel', onWheel, { passive: false });
    return () => {
      el.removeEventListener('wheel', onWheel);
    };
  }, []);

  const zoomBy = (factor: number) => {
    setView({ ...zoomAt(shown, shown.z * factor, size.w / 2, size.h / 2), user: true });
  };

  const onPointerDown = (e: PointerEvent<HTMLDivElement>) => {
    if (e.button !== 0 || (e.target as HTMLElement).closest('[data-gid], [data-zoom]')) return;
    drag.current = { x: e.clientX, y: e.clientY, tx: shown.tx, ty: shown.ty };
    setPanning(true);
    e.currentTarget.setPointerCapture(e.pointerId);
  };
  const onPointerMove = (e: PointerEvent<HTMLDivElement>) => {
    const d = drag.current;
    if (!d) return;
    setView({ z: shown.z, tx: d.tx + e.clientX - d.x, ty: d.ty + e.clientY - d.y, user: true });
  };
  const onPointerUp = () => {
    drag.current = null;
    setPanning(false);
  };

  const ordered = model.steps.map((s) => s.node.id);
  const tabStop = focusId && layout.pos.has(focusId) ? focusId : (stepId ?? ordered[0] ?? null);

  const focusCard = (id: string) => {
    setFocusId(id);
    const p = layout.pos.get(id);
    const next = p ? revealCard(shown, p, heightOf(), size.w, size.h) : null;
    if (next) setView({ ...next, user: true });
    viewport.current
      ?.querySelector<HTMLElement>(`[data-gid="${CSS.escape(id)}"]`)
      ?.focus({ preventScroll: true });
  };

  const onKey = (e: KeyboardEvent<HTMLDivElement>) => {
    const card = (e.target as HTMLElement).closest<HTMLElement>('[data-gid]');
    const id = card?.dataset.gid;
    if (!id) return;
    let next: string | null = null;
    if (
      e.key === 'ArrowLeft' ||
      e.key === 'ArrowRight' ||
      e.key === 'ArrowUp' ||
      e.key === 'ArrowDown'
    ) {
      e.preventDefault();
      next = graphMove(layout, id, e.key);
    } else if (e.key === 'Home' || e.key === 'End') {
      e.preventDefault();
      next = (e.key === 'Home' ? ordered[0] : ordered[ordered.length - 1]) ?? null;
    } else if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault();
      onSelect(id);
      return;
    }
    if (next) focusCard(next);
  };

  const width = layout.width;
  const height = layout.height + GAP_Y * 2;
  const pct = `${String(Math.round(shown.z * 100))}%`;

  return (
    <section
      aria-label={copy.runs.view.run}
      data-ui-canvas
      style={{
        contain: 'paint',
        backgroundImage:
          'repeating-linear-gradient(135deg, var(--canvas-stripe) 0 1px, transparent 1px 12px)',
      }}
      className="relative min-h-16 flex-auto bg-bg"
    >
      <div
        ref={viewport}
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={onPointerUp}
        onPointerCancel={onPointerUp}
        onKeyDown={onKey}
        style={{ touchAction: 'none', cursor: panning ? 'grabbing' : 'grab' }}
        className="absolute inset-0"
      >
        <div
          style={{
            width,
            height,
            transform: `translate(${String(shown.tx)}px, ${String(shown.ty)}px) scale(${String(shown.z)})`,
            transformOrigin: '0 0',
          }}
          className="absolute top-0 left-0"
        >
          <svg
            aria-hidden="true"
            width={width}
            height={height}
            viewBox={`0 ${String(-GAP_Y)} ${String(width)} ${String(height)}`}
            className="pointer-events-none absolute top-0 left-0 overflow-visible"
          >
            {def.edges.map((e) => {
              const d = edgePath(layout, e.source, e.target, heightOf, shown.z);
              if (!d) return null;
              const dim = !model.byNode.get(e.target)?.nodeRun;
              return (
                <path
                  key={e.id}
                  d={d}
                  fill="none"
                  stroke={dim ? 'var(--border)' : 'var(--border-control)'}
                  strokeWidth={lod ? 1.5 / shown.z : 1.5}
                  strokeDasharray={dim ? '4 4' : undefined}
                />
              );
            })}
          </svg>
          <div
            role="tree"
            aria-label={g.label(model.snapshot.run.definitionVersion)}
            style={{ top: GAP_Y }}
            className="absolute inset-x-0"
          >
            {model.steps.map((step) => {
              const p = layout.pos.get(step.node.id);
              if (!p) return null;
              const info = statusInfo({ kind: 'step', value: step.statusKey });
              const sel = selectedId === step.node.id;
              const within = !sel && stepId === step.node.id;
              const meta = metaText(step);
              const Icon = stepIcon(step.node.type);
              const card = (
                <div
                  role="treeitem"
                  data-gid={step.node.id}
                  aria-selected={sel}
                  aria-label={g.cardLabel(step.node.name, info.label, meta)}
                  tabIndex={tabStop === step.node.id ? 0 : -1}
                  onFocus={() => {
                    setFocusId(step.node.id);
                  }}
                  onClick={() => {
                    onSelect(step.node.id);
                  }}
                  style={{
                    left: p.x,
                    top: p.y,
                    width: CARD_W,
                    ...(lod ? { height: lod } : { minHeight: CARD_H }),
                  }}
                  className={cn(
                    'absolute flex cursor-pointer flex-col rounded-lg border bg-surface text-left shadow-card hover:border-fg-muted focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-focus',
                    lod ? 'justify-center' : 'gap-2 px-3 py-2',
                    step.nodeRun ? 'border-solid' : 'border-dashed',
                    sel
                      ? 'border-accent bg-surface-raised'
                      : within
                        ? 'border-accent'
                        : 'border-border',
                  )}
                >
                  {sel ? (
                    <span
                      aria-hidden="true"
                      className="pointer-events-none absolute -inset-px rounded-lg border-2 border-accent"
                    />
                  ) : null}
                  <span
                    style={
                      lod
                        ? {
                            width: CARD_W * shown.z,
                            transform: `scale(${String(1 / shown.z)})`,
                            transformOrigin: 'left center',
                          }
                        : undefined
                    }
                    className={cn(
                      'flex min-w-0',
                      lod ? 'items-center gap-1.5 px-1.5' : 'items-start gap-2',
                    )}
                  >
                    {level === 'far' ? (
                      <Icon
                        aria-hidden="true"
                        className="size-icon-xs shrink-0 text-fg-secondary"
                      />
                    ) : (
                      <span
                        className={cn(
                          'inline-flex shrink-0 items-center justify-center rounded-md',
                          lod ? 'size-icon-lg' : 'size-control-xs',
                          sel
                            ? 'bg-surface-selected text-accent-fg'
                            : 'bg-surface-hover text-fg-secondary',
                        )}
                      >
                        <Icon aria-hidden="true" className="size-icon-sm" />
                      </span>
                    )}
                    <span className="flex min-w-0 flex-1 flex-col">
                      <span
                        className={cn(
                          'font-semibold wrap-anywhere',
                          step.nodeRun ? 'text-fg' : 'text-fg-secondary',
                          lod ? (level === 'far' ? 'text-xs' : 'text-sm') : 'text-md',
                        )}
                      >
                        {step.node.name}
                      </span>
                      {lod ? null : <span className="text-sm text-fg-muted">{step.typeLabel}</span>}
                    </span>
                    <span className={cn('inline-flex shrink-0', !lod && 'mt-0.5')}>
                      <StatusIndicator
                        size="icon"
                        status={{ kind: 'step', value: step.statusKey }}
                      />
                    </span>
                  </span>
                  {lod ? null : (
                    <span className="pl-8 text-sm text-fg-secondary">
                      <CardMeta model={model} step={step} />
                    </span>
                  )}
                </div>
              );
              return lod ? (
                <Tooltip
                  key={step.node.id}
                  content={meta ? `${step.node.name}: ${meta}` : step.node.name}
                >
                  {card}
                </Tooltip>
              ) : (
                <span key={step.node.id} className="contents">
                  {card}
                </span>
              );
            })}
          </div>
        </div>
      </div>
      <div
        role="group"
        aria-label={g.zoom}
        data-zoom
        className="absolute bottom-4 left-4 z-canvas-ui flex items-center gap-0.5 rounded-lg border border-border bg-surface-raised p-1 shadow-2"
      >
        <IconButton
          label={g.zoomOut}
          unavailableReason={shown.z <= ZOOM_MIN + 0.001 ? g.zoomOut : undefined}
          onClick={() => {
            zoomBy(1 / 1.2);
          }}
        >
          <Minus aria-hidden="true" className="size-icon-md" />
        </IconButton>
        <span
          aria-live="polite"
          className="min-w-10 text-center text-sm text-fg-secondary tabular-nums"
        >
          {pct}
        </span>
        <IconButton
          label={g.zoomIn}
          unavailableReason={shown.z >= ZOOM_MAX - 0.001 ? g.zoomIn : undefined}
          onClick={() => {
            zoomBy(1.2);
          }}
        >
          <Plus aria-hidden="true" className="size-icon-md" />
        </IconButton>
        <IconButton
          label={g.fit}
          onClick={() => {
            setView((v) => ({ ...v, user: false }));
          }}
        >
          <Maximize aria-hidden="true" className="size-icon-md" />
        </IconButton>
      </div>
    </section>
  );
}
