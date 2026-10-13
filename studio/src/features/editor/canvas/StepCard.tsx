import type { NodeProps, Node as FlowNode } from '@xyflow/react';
import { ChevronDown, TriangleAlert } from 'lucide-react';
import { memo, useLayoutEffect, useRef, useState } from 'react';

import type { Node, NodeMetadata } from '@/api/types';
import { StatusIndicator } from '@/components/status/StatusIndicator';
import { statusInfo } from '@/components/status/status-info';
import { IconButton } from '@/components/ui/button';
import { Tooltip } from '@/components/ui/tooltip';
import { useCopy } from '@/lib/copy';
import { duration } from '@/lib/time';
import { cn } from '@/lib/utils';
import { useEditor } from '@/features/editor/editor-context';
import { useEditorStore } from '@/features/editor/editor-store';
import { issuesFor } from '@/features/editor/model/checks';
import { CARD_W } from '@/features/editor/model/layout';
import { groupOf, portLabel } from '@/features/editor/model/steps';
import { StepIcon } from '@/features/editor/StepIcon';
import { CardBody } from './CardBody';
import { portsY, useCanvasView } from './canvas-view';
import { InPort, OutPort } from './Ports';

export type StepFlowNode = FlowNode<{ nodeId: string }, 'step'>;

/** The last run's status on a card; the numbers sit in its tooltip. */
function LastRunStatus({ nodeId }: { nodeId: string }) {
  const copy = useCopy();
  const t = copy.editor.canvas;
  const { lastRun } = useEditor();
  if (!lastRun.snapshot) return null;
  const nr = lastRun.byNode.get(nodeId);
  if (!nr && lastRun.snapshot.run.status !== 'FAILED') return null;
  const value = nr ? nr.status : 'NONE_RUN_FAILED';
  const help = nr
    ? t.lastRunTip(
        statusInfo({ kind: 'step', value }).label,
        nr.latencyMs !== null ? duration(nr.latencyMs) : null,
        nr.tokenUsage?.totalTokens ?? null,
      )
    : t.lastRunNotReached;
  return (
    <StatusIndicator status={{ kind: 'step', value }} size="icon" help={help} className="px-0.5" />
  );
}

function IssueMarker({ count }: { count: number }) {
  const copy = useCopy();
  if (!count) return null;
  return (
    <Tooltip content={copy.editor.checks.cardTip(count)}>
      <span
        role="img"
        aria-label={copy.editor.checks.count(count)}
        className="inline-flex h-control-xs items-center gap-1 rounded-sm bg-caution-bg px-1.5 text-sm font-medium text-fg tabular-nums"
      >
        <TriangleAlert aria-hidden="true" className="size-icon-xs text-caution-fg" />
        <span>{count}</span>
      </span>
    </Tooltip>
  );
}

/** The step's name; double-click renames it in place on a full card. */
function CardName({ node, editable, lod }: { node: Node; editable: boolean; lod: boolean }) {
  const copy = useCopy();
  const { ops } = useEditor();
  const view = useCanvasView();
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(node.name);
  if (editing) {
    return (
      <input
        aria-label={copy.editor.panel.stepName}
        autoFocus
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        onKeyDown={(e) => {
          e.stopPropagation();
          if (e.key === 'Enter') e.currentTarget.blur();
          if (e.key === 'Escape') {
            setDraft(node.name);
            setEditing(false);
            view.onRenameEnd();
          }
        }}
        onBlur={() => {
          ops.rename(node.id, draft);
          setEditing(false);
          view.onRenameEnd();
        }}
        className="nodrag w-full rounded-sm border border-focus bg-surface-inset px-1 text-md font-semibold text-fg outline-none"
      />
    );
  }
  return (
    <span
      onDoubleClick={(e) => {
        if (!editable) return;
        e.stopPropagation();
        setDraft(node.name);
        setEditing(true);
      }}
      className={cn(
        'font-semibold text-fg',
        lod ? (view.level === 'far' ? 'text-xs' : 'text-sm') : 'text-md wrap-anywhere',
        editable && 'cursor-text',
      )}
    >
      {node.name}
    </span>
  );
}

/**
 * A step on the canvas: its head (icon, name, type, issues, last-run status) and, on a full
 * card, read-only rows that open the matching field in the side panel. Below the full-card
 * zoom every card is one header row drawn at its on-screen size, widened so the name is never
 * cut short.
 */
function StepCardInner({ data, selected }: NodeProps<StepFlowNode>) {
  const copy = useCopy();
  const t = copy.editor;
  const { registry, issues, openSettings, quickAdd } = useEditor();
  const view = useCanvasView();
  const node = useEditorStore((s) => s.doc.nodes.find((n) => n.id === data.nodeId));
  const edges = useEditorStore((s) => s.doc.edges);
  const headRef = useRef<HTMLDivElement>(null);
  const [lodSize, setLodSize] = useState<{ w: number; h: number; wrap: boolean } | null>(null);

  const lod = view.level !== 'full';
  const place = view.places[data.nodeId];
  const placeKey = place ? `${String(place.x)},${String(place.y)}` : '';
  const roomKey = Object.values(view.places)
    .filter((q) => place && q.x > place.x + 1)
    .map((q) => q.x)
    .join(',');

  // One-row cards never cut a name short: the card grows right into the column gap, and wraps
  // the name at word boundaries when the next column is in the way.
  useLayoutEffect(() => {
    const head = headRef.current;
    if (!lod || !head || !place) {
      setLodSize(null);
      return;
    }
    const z = view.zoom;
    head.style.width = 'max-content';
    const need = head.offsetWidth;
    head.style.width = 'min-content';
    const least = head.offsetWidth;
    head.style.width = '';
    const clear = (view.level === 'far' ? 8 : 24) / z;
    let room = Infinity;
    let edge = Infinity;
    for (const q of Object.values(view.places)) {
      if (q.x <= place.x + 1) continue;
      room = Math.min(room, q.x - place.x - clear);
      edge = Math.min(edge, q.x - place.x - 4 / z);
    }
    let w = Math.max(CARD_W, Math.ceil((need + 2) / z));
    let wrap = false;
    if (w > room) {
      w = Math.max(CARD_W, Math.floor(Math.max(room, Math.min(edge, Math.ceil((least + 2) / z)))));
      wrap = true;
    }
    setLodSize({ w, h: view.lodH, wrap });
    // A wrapped name makes the row taller; measured after the width applies.
    // eslint-disable-next-line react-hooks/exhaustive-deps -- placeKey and roomKey stand for the places read here
  }, [lod, view.zoom, view.level, view.lodH, node?.name, placeKey, roomKey]);

  useLayoutEffect(() => {
    const head = headRef.current;
    if (!lodSize?.wrap || !head) return;
    const h = Math.max(view.lodH, Math.ceil((head.offsetHeight + 12) / view.zoom));
    if (h !== lodSize.h) setLodSize({ ...lodSize, h });
  }, [lodSize, view.lodH, view.zoom]);

  if (!node) return null;
  const meta: NodeMetadata | undefined = registry.catalog.get(node.type);
  const count = issuesFor(issues, node.id).length;
  const collapsed = lod || view.collapsed.has(node.id);
  const typeName = meta?.displayName ?? node.type;
  const showType = !lod && node.name.trim().toLowerCase() !== typeName.toLowerCase();
  const inputs = meta?.inputs ?? [];
  const outputs = meta?.outputs ?? [];
  const outFree = (name: string) =>
    !edges.some((e) => e.source === node.id && e.sourceHandle === name);
  const far = view.level === 'far';
  const z = view.zoom;
  const cardH = lod ? (lodSize?.h ?? view.lodH) : collapsed ? 56 : undefined;

  const addFrom =
    (port: string, type: (typeof outputs)[number]['dataType']) =>
    (e: React.MouseEvent<HTMLButtonElement>) => {
      e.stopPropagation();
      const r = e.currentTarget.getBoundingClientRect();
      quickAdd({
        x: r.right + 8,
        y: r.top,
        link: { kind: 'from', sid: node.id, handle: port, type },
      });
    };

  const head = (
    <div
      ref={headRef}
      data-card-head
      style={
        lod
          ? {
              width: `calc(100% * ${String(z)})`,
              transform: `scale(${String(1 / z)})`,
              transformOrigin: 'left center',
            }
          : collapsed
            ? { minHeight: 56 }
            : undefined
      }
      className={cn(
        'flex min-w-0',
        lod
          ? cn('shrink-0 items-center px-1.5', far ? 'gap-1' : 'gap-1.5')
          : 'items-start gap-2 py-2 pr-2 pl-3',
      )}
    >
      <span
        className={cn(
          'inline-flex shrink-0 items-center justify-center rounded-md',
          far
            ? ''
            : selected
              ? 'bg-surface-selected text-accent-fg'
              : 'bg-surface-hover text-fg-secondary',
          lod ? (far ? 'text-fg-secondary' : 'size-5') : 'size-control-xs',
        )}
      >
        <StepIcon
          type={node.type}
          group={groupOf(meta, node.type)}
          className={far ? 'size-icon-xs' : 'size-icon-sm'}
        />
      </span>
      <span className={cn('flex min-w-0 flex-1 flex-col', !lod && 'pt-0.5')}>
        <span
          className={cn(
            lod && (lodSize?.wrap ? 'break-normal wrap-break-word' : 'whitespace-nowrap'),
          )}
        >
          <CardName node={node} editable={!lod} lod={lod} />
        </span>
        {showType ? <span className="text-sm wrap-anywhere text-fg-muted">{typeName}</span> : null}
      </span>
      <span
        className={cn(
          'inline-flex shrink-0 items-center',
          lod ? 'gap-0.5' : 'min-h-control-xs gap-1',
        )}
      >
        <IssueMarker count={count} />
        <LastRunStatus nodeId={node.id} />
        {lod ? null : (
          <IconButton
            size="xs"
            tabIndex={-1}
            label={collapsed ? t.canvas.showDetails(node.name) : t.canvas.hideDetails(node.name)}
            tooltip={collapsed ? t.canvas.showDetailsTip : t.canvas.hideDetailsTip}
            aria-expanded={!collapsed}
            className="nodrag"
            onClick={(e) => {
              e.stopPropagation();
              view.toggleCollapsed(node.id);
            }}
          >
            <ChevronDown
              aria-hidden="true"
              className={cn('transition-transform duration-1', collapsed && '-rotate-90')}
            />
          </IconButton>
        )}
      </span>
    </div>
  );

  return (
    <div
      data-node={node.id}
      data-step-card
      data-zoom-level={view.level}
      tabIndex={view.tabStop === node.id ? 0 : -1}
      role="group"
      aria-roledescription={t.canvas.roleStep}
      aria-label={t.canvas.cardLabel(node.name, typeName, selected)}
      style={{ width: lod ? (lodSize?.w ?? CARD_W) : CARD_W, height: cardH }}
      className={cn(
        'group/card relative flex flex-col rounded-lg border bg-surface text-left shadow-card transition-colors duration-1 focus-visible:outline-2 focus-visible:outline-offset-3 focus-visible:outline-focus',
        selected ? 'border-accent outline-1 outline-accent' : 'border-border hover:border-fg-muted',
        lod && 'justify-center',
        view.panning && 'cursor-grab',
      )}
    >
      {head}
      {collapsed ? (
        <>
          {inputs.map((p, i) => (
            <InPort
              key={p.name}
              port={p}
              top={portsY(cardH ?? 56, inputs.length, i)}
              hidden={far}
              label
            />
          ))}
          {outputs.map((p, i) => (
            <OutPort
              key={p.name}
              nodeId={node.id}
              nodeName={node.name}
              port={p}
              free={outFree(p.name) && !lod}
              top={portsY(cardH ?? 56, outputs.length, i)}
              hidden={far}
              label
              showAdd={selected && !view.panning}
              onAdd={addFrom(p.name, p.dataType)}
            />
          ))}
        </>
      ) : (
        <>
          {meta ? <CardBody node={node} meta={meta} /> : null}
          <div className="flex min-h-control-md items-center gap-2 border-t border-border-subtle pr-3 pl-2">
            <button
              type="button"
              tabIndex={-1}
              onClick={(e) => {
                e.stopPropagation();
                openSettings(node.id, null);
              }}
              className="nodrag h-control-xs rounded-sm px-1 text-sm whitespace-nowrap text-fg-muted hover:text-accent-fg hover:underline"
            >
              {t.canvas.allSettings}
            </button>
            <span className="ml-auto flex flex-col items-end">
              {outputs.map((p) => (
                <span
                  key={p.name}
                  className="relative flex min-h-control-xs items-center text-sm text-fg-secondary"
                >
                  {portLabel(p)}
                  <OutPort
                    nodeId={node.id}
                    nodeName={node.name}
                    port={p}
                    free={outFree(p.name)}
                    right={-13}
                    showAdd={selected && !view.panning}
                    onAdd={addFrom(p.name, p.dataType)}
                  />
                </span>
              ))}
            </span>
          </div>
        </>
      )}
    </div>
  );
}

export const StepCard = memo(StepCardInner);
