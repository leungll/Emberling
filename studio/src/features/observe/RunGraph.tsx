import { Maximize, Minus, Plus } from 'lucide-react';
import {
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
  type PointerEvent,
} from 'react';

import { cardInfo, type CardInfo } from './card-info';
import { ElementTime } from './RunFacts';
import { mediaItems } from './run-media';
import { Words } from './Words';
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
import { ThumbSet } from '@/components/media/Thumbs';
import { IconButton } from '@/components/ui/button';
import { Tooltip } from '@/components/ui/tooltip';
import { useCopy } from '@/lib/copy';
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

/** The plain words of a card after its status: the activity line, the turn and the time. */
function metaText(info: CardInfo, step: Step): string {
  const nr = step.nodeRun;
  const parts = [info.line.text];
  if (info.turn) parts.push(info.turn);
  if (nr?.latencyMs !== null && nr?.latencyMs !== undefined) parts.push(duration(nr.latencyMs));
  return parts.join(', ');
}

/**
 * A full card's body: the activity line (a text preview clamped to two lines, its full
 * text in the tooltip and the step's Output tab), the muted turn and time, then up to four
 * thumbnails with the rest behind "+N".
 */
function CardBody({ model, step, info }: { model: RunModel; step: Step; info: CardInfo }) {
  const el = model.byId.get(step.node.id);
  const text = (
    <span
      data-ui-exempt={
        info.clamp ? 'overflow:text-preview-full-text-in-tooltip-and-output' : undefined
      }
      className={cn(
        'pl-8 text-sm wrap-anywhere',
        info.idle ? 'text-fg-muted' : info.clamp ? 'line-clamp-2 text-fg-secondary' : 'text-fg',
      )}
    >
      <Words text={info.line.text} tools={info.line.tools} />
    </span>
  );
  const time = el && step.nodeRun ? <ElementTime model={model} el={el} /> : null;
  return (
    <>
      {info.full ? <Tooltip content={info.full}>{text}</Tooltip> : text}
      {info.turn || time ? (
        <span className="pl-8 text-sm text-fg-muted">
          {info.turn}
          {info.turn && time ? ', ' : null}
          {time}
        </span>
      ) : null}
      {info.refs.length ? (
        <ThumbSet
          items={mediaItems(model, info.refs)}
          max={4}
          label={info.refsLabel ?? undefined}
          tabbable={false}
          className="pl-8"
        />
      ) : null}
    </>
  );
}

export function RunGraph({ model, selectedId, onSelect }: RunGraphProps) {
  const copy = useCopy();
  const g = copy.runs.graph;
  const def = model.definition;
  const [heights, setHeights] = useState<ReadonlyMap<string, number>>(new Map());
  const layout = useMemo(() => graphLayout(def, (id) => heights.get(id) ?? CARD_H), [def, heights]);
  const infos = useMemo(
    () => new Map(model.steps.map((s) => [s.node.id, cardInfo(model, s)])),
    [model],
  );
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
  const heightOf = (id: string) => (lod ? lod : (heights.get(id) ?? CARD_H));

  // Full cards are as tall as their content: measure them before paint, and again whenever
  // one changes size (a font arriving, a line that rewraps), and lay the columns out anew.
  useLayoutEffect(() => {
    const root = viewport.current;
    if (lod || !root) return;
    const cards = Array.from(root.querySelectorAll<HTMLElement>('[data-gid]'));
    const measure = () => {
      setHeights((prev) => {
        let changed = prev.size !== cards.length;
        const next = new Map<string, number>();
        for (const c of cards) {
          const id = c.dataset.gid ?? '';
          const h = c.offsetHeight;
          next.set(id, h);
          if (prev.get(id) !== h) changed = true;
        }
        return changed ? next : prev;
      });
    };
    measure();
    const ro = new ResizeObserver(measure);
    for (const c of cards) ro.observe(c);
    return () => {
      ro.disconnect();
    };
  });

  // Keep the selected step in view: pan the least distance that shows its card.
  const stepId = stepElementOf(model.byId.get(selectedId))?.id ?? null;
  if (stepId !== revealed && size.w) {
    setRevealed(stepId);
    const p = stepId ? layout.pos.get(stepId) : undefined;
    const next = p && stepId ? revealCard(shown, p, heightOf(stepId), size.w, size.h) : null;
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
    const target = e.target as HTMLElement;
    // A card's lightbox is portalled out of the graph but its events still bubble here
    // through React; capturing the pointer for them would swallow the lightbox's clicks.
    if (!e.currentTarget.contains(target)) return;
    if (e.button !== 0 || target.closest('[data-gid], [data-zoom]')) return;
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
    const next = p ? revealCard(shown, p, heightOf(id), size.w, size.h) : null;
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
              // A link to a step that has not run is dashed, never fainter: the faint
              // border token all but vanishes on the striped canvas.
              return (
                <path
                  key={e.id}
                  d={d}
                  fill="none"
                  stroke="var(--border-control)"
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
              const activity = infos.get(step.node.id) ?? cardInfo(model, step);
              const meta = metaText(activity, step);
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
                  onClick={(e) => {
                    if (e.currentTarget.contains(e.target as Node)) onSelect(step.node.id);
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
                        : step.nodeRun
                          ? 'border-border'
                          : 'border-border-control',
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
                  {lod ? null : <CardBody model={model} step={step} info={activity} />}
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
