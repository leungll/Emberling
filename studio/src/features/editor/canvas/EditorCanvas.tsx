import {
  ReactFlow,
  ReactFlowProvider,
  useReactFlow,
  useStore as useFlowStore,
  type ConnectionLineComponentProps,
  type EdgeChange,
  type NodeChange,
} from '@xyflow/react';
import { AlertTriangle, Info, Plus, Workflow } from 'lucide-react';
import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type DragEvent,
} from 'react';

import { Button } from '@/components/ui/button';
import { useCopy } from '@/lib/copy';
import { cn } from '@/lib/utils';
import { useEditor, type Geometry } from '@/features/editor/editor-context';
import { useEditorStore } from '@/features/editor/editor-store';
import {
  CARD_H,
  CARD_W,
  boundsOf,
  curve,
  layered,
  type PlaceMap,
} from '@/features/editor/model/layout';
import { ordered } from '@/features/editor/model/steps';
import { ZOOM_MAX, ZOOM_MIN, lodCardHeight, zoomLevel } from '@/features/editor/model/view';
import { CanvasMenu, type MenuAction, type MenuRequest } from './CanvasMenu';
import { Minimap, ToolBar, ZoomPill, type CanvasTool } from './CanvasControls';
import {
  CanvasViewContext,
  LinkToolsContext,
  STEP_MIME,
  type CanvasView,
  type LinkTools,
} from './canvas-view';
import { LinkEdge, type LinkFlowEdge } from './LinkEdge';
import { StepCard, type StepFlowNode } from './StepCard';
import { StepToolbar } from './StepToolbar';
import { useCanvasKeys } from './useCanvasKeys';
import { useCanvasViewport } from './useCanvasViewport';
import { useLinkDrag, type LinkHint } from './useLinkDrag';

const NODE_TYPES = { step: StepCard };
const EDGE_TYPES = { link: LinkEdge };
const NARROW = 760;

/** The dashed link that follows the pointer while connecting. */
function DraftLink({ fromX, fromY, toX, toY }: ConnectionLineComponentProps) {
  const zoom = useFlowStore((s) => s.transform[2]);
  const lod = zoomLevel(zoom) !== 'full';
  return (
    <path
      d={curve(fromX, fromY, toX, toY)}
      fill="none"
      style={{ stroke: 'var(--accent)', strokeWidth: 2 / (lod ? zoom : 1), strokeDasharray: '5 4' }}
    />
  );
}

function HintBubble({ hint }: { hint: LinkHint }) {
  const ref = useRef<HTMLDivElement>(null);
  // Beside the pointer, flipped to the other side near the window's edges.
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    let left = hint.x + 16;
    let top = hint.y + 20;
    if (left + el.offsetWidth > window.innerWidth - 8) left = hint.x - el.offsetWidth - 16;
    if (top + el.offsetHeight > window.innerHeight - 8) top = hint.y - el.offsetHeight - 16;
    el.style.left = `${String(Math.max(8, left))}px`;
    el.style.top = `${String(Math.max(8, top))}px`;
  }, [hint]);
  return (
    <div
      ref={ref}
      role="status"
      style={{ position: 'fixed', left: hint.x + 16, top: hint.y + 20, maxWidth: 320 }}
      className="pointer-events-none z-popover flex items-center gap-1.5 rounded-lg border border-border bg-surface-raised px-3 py-2 text-sm text-fg shadow-1"
    >
      {hint.tone === 'bad' ? (
        <AlertTriangle aria-hidden="true" className="size-icon-sm shrink-0 text-caution-fg" />
      ) : hint.tone === 'busy' ? (
        <Info aria-hidden="true" className="size-icon-sm shrink-0 text-fg-muted" />
      ) : null}
      <span>{hint.text}</span>
    </div>
  );
}

function EmptyCanvas({ onAdd }: { onAdd: (e: React.MouseEvent<HTMLButtonElement>) => void }) {
  const copy = useCopy();
  const t = copy.editor.canvas;
  return (
    <div className="pointer-events-none absolute inset-0 z-canvas-ui flex items-center justify-center">
      <div className="pointer-events-auto flex flex-col items-center gap-2 px-6 text-center">
        <Workflow aria-hidden="true" className="mb-1 size-icon-lg text-fg-muted" />
        <p className="m-0 text-md font-medium text-fg">{t.empty}</p>
        <Button data-add-first onClick={onAdd}>
          <Plus aria-hidden="true" />
          {t.addFirst}
        </Button>
        <p style={{ maxWidth: '40ch' }} className="m-0 text-sm text-fg-muted">
          {t.emptyHint}
        </p>
      </div>
    </div>
  );
}

interface CanvasProps {
  /** The link a step is being inserted into, while quick add is open for it. */
  inserting: string | null;
  /** The opening view can't show every step: the steps sidebar may collapse to make room. */
  onOverflow: () => void;
  /** Receives where things are drawn, for edits that place steps and for focus moves. */
  onGeometry: (g: Geometry) => void;
}

function Canvas({ inserting, onOverflow, onGeometry }: CanvasProps) {
  const copy = useCopy();
  const t = copy.editor;
  const ctx = useEditor();
  const { registry, ops, quickAdd, openSettings } = ctx;
  const rf = useReactFlow<StepFlowNode, LinkFlowEdge>();
  const zoom = useFlowStore((s) => s.transform[2]);
  const nodes = useEditorStore((s) => s.doc.nodes);
  const edges = useEditorStore((s) => s.doc.edges);
  const selection = useEditorStore((s) => s.selection);
  const autoLayout = useEditorStore((s) => s.autoLayout);
  const heights = useEditorStore((s) => s.heights);
  const canUndo = useEditorStore((s) => s.history.undo.length > 0);
  const canRedo = useEditorStore((s) => s.history.redo.length > 0);

  const areaRef = useRef<HTMLDivElement>(null);
  const [tool, setTool] = useState<CanvasTool>('select');
  const [collapsed, setCollapsed] = useState<ReadonlySet<string>>(() => new Set());
  const [measured, setMeasured] = useState<Record<string, { w: number; h: number }>>({});
  const [drag, setDrag] = useState<Record<string, { x: number; y: number }>>({});
  const [movingCards, setMovingCards] = useState(false);
  const [hoverEdge, setHoverEdge] = useState<string | null>(null);
  const [menu, setMenu] = useState<MenuRequest | null>(null);
  const [lastFocus, setLastFocus] = useState<string | null>(null);
  const [fileOver, setFileOver] = useState(false);
  const pointer = useRef<{ x: number; y: number } | null>(null);
  const pointerDown = useRef(false);
  const quietFocus = useRef(false);
  const dragRef = useRef(drag);
  useLayoutEffect(() => {
    dragRef.current = drag;
  });

  const level = zoomLevel(zoom);
  const lod = level !== 'full';
  const lodH = lodCardHeight(zoom);

  // Positions come from full cards, so changing the zoom level never moves a step.
  const base = useMemo(
    () =>
      autoLayout
        ? layered(nodes, edges, (id) => heights[id] ?? CARD_H)
        : Object.fromEntries(nodes.map((n) => [n.id, { x: n.position.x, y: n.position.y }])),
    [autoLayout, nodes, edges, heights],
  );
  const places = useMemo<PlaceMap>(() => {
    const out: PlaceMap = {};
    for (const n of nodes) {
      const p = drag[n.id] ?? base[n.id] ?? { x: 0, y: 0 };
      const m = measured[n.id];
      out[n.id] = {
        x: p.x,
        y: p.y,
        w: m?.w ?? CARD_W,
        h: m?.h ?? (lod ? lodH : (heights[n.id] ?? CARD_H)),
      };
    }
    return out;
  }, [nodes, drag, base, measured, lod, lodH, heights]);
  const placesRef = useRef(places);
  useLayoutEffect(() => {
    placesRef.current = places;
  });

  const bounds = useMemo(
    () => boundsOf(base, (id) => heights[id] ?? measured[id]?.h ?? CARD_H),
    [base, heights, measured],
  );
  const ready = registry.loaded && nodes.every((n) => measured[n.id]);

  const vp = useCanvasViewport({ areaRef, bounds, ready, onOverflow });
  const narrow = vp.size.w > 0 && vp.size.w <= NARROW;

  // Any edit leaves the view where the user put it.
  useEffect(
    () =>
      useEditorStore.subscribe((s, prev) => {
        if (s.history !== prev.history && s.doc !== prev.doc) vp.userMoved();
      }),
    [vp],
  );

  const toFlow = useCallback((p: { x: number; y: number }) => rf.screenToFlowPosition(p), [rf]);
  const link = useLinkDrag({ areaRef, catalog: registry.catalog, ops, toFlow, quickAdd });

  // Full-card heights feed the computed layout and Fit view.
  useEffect(() => {
    if (lod) return;
    const next: Record<string, number> = {};
    for (const n of nodes) {
      const m = measured[n.id];
      if (m && !collapsed.has(n.id)) next[n.id] = m.h;
    }
    if (Object.keys(next).length) useEditorStore.getState().setHeights(next);
  }, [lod, measured, nodes, collapsed]);

  const order = useMemo(() => ordered(nodes).map((n) => n.id), [nodes]);
  const tabStop =
    selection.ids.find((id) => places[id]) ??
    (lastFocus && places[lastFocus] ? lastFocus : (order[0] ?? null));

  const cardEl = useCallback(
    (id: string) =>
      areaRef.current?.querySelector<HTMLElement>(
        `[data-step-card][data-node="${CSS.escape(id)}"]`,
      ) ?? null,
    [],
  );
  const focusCard = useCallback(
    (id: string) => {
      setLastFocus(id);
      requestAnimationFrame(() => cardEl(id)?.focus({ preventScroll: true }));
    },
    [cardEl],
  );
  // Returning focus to the canvas keeps the selection: focus selects a card only when the
  // viewer moved there themselves.
  const focusCanvas = useCallback(() => {
    const s = useEditorStore.getState().selection.ids[0];
    const id = (s && placesRef.current[s] ? s : null) ?? tabStop;
    const el = id ? cardEl(id) : null;
    if (!el) return;
    quietFocus.current = true;
    try {
      el.focus({ preventScroll: true });
    } finally {
      quietFocus.current = false;
    }
  }, [cardEl, tabStop]);

  useEffect(() => {
    onGeometry({
      places,
      portY: (id, dir, handle) => {
        const n = rf.getInternalNode(id);
        const list =
          dir === 'in' ? n?.internals.handleBounds?.target : n?.internals.handleBounds?.source;
        const hb = list?.find((h) => h.id === handle);
        return hb ? hb.y + hb.height / 2 : CARD_H / 2;
      },
      center: vp.centrePoint,
      reveal: (id) => vp.reveal(placesRef.current, id),
      focusCard,
      focusCanvas,
    });
  });

  const toggleCollapsed = useCallback((id: string) => {
    setCollapsed((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }, []);

  const view = useMemo<CanvasView>(
    () => ({
      level,
      zoom,
      lodH,
      collapsed,
      toggleCollapsed,
      tabStop,
      places,
      panning: tool === 'pan',
      dragging: link.dragging,
      remeasure: () => undefined,
      onRenameEnd: () => focusCanvas(),
    }),
    [
      level,
      zoom,
      lodH,
      collapsed,
      toggleCollapsed,
      tabStop,
      places,
      tool,
      link.dragging,
      focusCanvas,
    ],
  );

  const linkTools = useMemo<LinkTools>(
    () => ({
      hover: hoverEdge,
      setHover: setHoverEdge,
      inserting,
      hidden: movingCards || !!link.dragging || tool === 'pan' || !!menu,
    }),
    [hoverEdge, inserting, movingCards, link.dragging, tool, menu],
  );

  const selected = useMemo(() => new Set(selection.ids), [selection.ids]);
  const rfNodes = useMemo<StepFlowNode[]>(
    () =>
      nodes.map((n) => {
        const p = places[n.id] ?? { x: 0, y: 0 };
        const m = measured[n.id];
        return {
          id: n.id,
          type: 'step',
          position: { x: p.x, y: p.y },
          data: { nodeId: n.id },
          selected: selected.has(n.id),
          measured: m ? { width: m.w, height: m.h } : undefined,
        };
      }),
    [nodes, places, measured, selected],
  );
  const rfEdges = useMemo<LinkFlowEdge[]>(() => {
    const ids = new Set(nodes.map((n) => n.id));
    return edges
      .filter((e) => ids.has(e.source) && ids.has(e.target))
      .map((e) => ({
        id: e.id,
        type: 'link',
        source: e.source,
        target: e.target,
        sourceHandle: e.sourceHandle,
        targetHandle: e.targetHandle,
        selected: selection.edge === e.id,
      }));
  }, [nodes, edges, selection.edge]);

  const onNodesChange = useCallback((changes: NodeChange<StepFlowNode>[]) => {
    const dims: Record<string, { w: number; h: number }> = {};
    const pos: Record<string, { x: number; y: number }> = {};
    const store = useEditorStore.getState();
    let sel: Set<string> | null = null;
    for (const c of changes) {
      if (c.type === 'dimensions' && c.dimensions)
        dims[c.id] = { w: c.dimensions.width, h: c.dimensions.height };
      else if (c.type === 'position' && c.position) pos[c.id] = c.position;
      else if (c.type === 'select') {
        sel ??= new Set(store.selection.ids);
        if (c.selected) sel.add(c.id);
        else sel.delete(c.id);
      }
    }
    if (Object.keys(dims).length) {
      setMeasured((prev) => {
        const same = Object.entries(dims).every(
          ([id, d]) => prev[id]?.w === d.w && prev[id]?.h === d.h,
        );
        return same ? prev : { ...prev, ...dims };
      });
    }
    if (Object.keys(pos).length) setDrag((prev) => ({ ...prev, ...pos }));
    if (sel) {
      const next = [...sel];
      const cur = store.selection.ids;
      if (next.length !== cur.length || next.some((id) => !cur.includes(id))) store.select(next);
    }
  }, []);

  const onEdgesChange = useCallback((changes: EdgeChange<LinkFlowEdge>[]) => {
    const store = useEditorStore.getState();
    let picked: string | null = null;
    let cleared = false;
    for (const c of changes) {
      if (c.type !== 'select') continue;
      if (c.selected) picked = c.id;
      else if (store.selection.edge === c.id) cleared = true;
    }
    if (picked) store.select([], picked);
    else if (cleared) store.select(store.selection.ids, null);
  }, []);

  const commitDrag = useCallback(() => {
    const moves = dragRef.current;
    setMovingCards(false);
    if (!Object.keys(moves).length) return;
    const store = useEditorStore.getState();
    const changed =
      store.autoLayout ||
      Object.entries(moves).some(([id, p]) => {
        const at = store.doc.nodes.find((x) => x.id === id)?.position;
        return at?.x !== p.x || at?.y !== p.y;
      });
    if (changed) store.moveSteps(moves);
    setDrag({});
  }, []);

  const openMenuAt = useCallback(
    (x: number, y: number, target: MenuRequest['target'], keyboard = false) => {
      setMenu({ x, y, target, keyboard });
    },
    [],
  );

  const onMenu = (action: MenuAction) => {
    const m = menu;
    setMenu(null);
    if (!m) return;
    const store = useEditorStore.getState();
    const ids = m.target.kind === 'step' ? [...m.target.ids] : [];
    const at = rf.screenToFlowPosition({ x: m.x, y: m.y });
    if (action === 'settings' && ids[0]) openSettings(ids[0], null);
    if (action === 'duplicate') ops.duplicate(ids);
    if (action === 'copy') ops.copy(ids);
    if (action === 'delete') ops.deleteSteps(ids);
    if (action === 'insert' && m.target.kind === 'link')
      quickAdd({ x: m.x, y: m.y, link: { kind: 'insert', edgeId: m.target.edgeId } });
    if (action === 'deleteLink' && store.selection.edge) ops.deleteEdge(store.selection.edge);
    if (action === 'add') quickAdd({ x: m.x, y: m.y, at });
    if (action === 'paste') ops.paste(at);
    if (action === 'fit') vp.fit();
  };

  useCanvasKeys({
    areaRef,
    places: placesRef,
    pointer,
    tool,
    setTool,
    vp,
    menuOpen: !!menu,
    cancelLink: link.dragging ? link.cancel : null,
    focusCard,
    focusCanvas,
    openMenuAt,
  });

  const anyExpanded = nodes.some((n) => !collapsed.has(n.id));
  const onDrop = (e: DragEvent<HTMLDivElement>) => {
    setFileOver(false);
    const type = e.dataTransfer.getData(STEP_MIME);
    if (!type) return;
    e.preventDefault();
    ops.addStep(type, rf.screenToFlowPosition({ x: e.clientX, y: e.clientY }));
  };

  return (
    <CanvasViewContext.Provider value={view}>
      <LinkToolsContext.Provider value={linkTools}>
        <div
          ref={areaRef}
          data-ui-canvas
          data-tool={tool}
          className={cn(
            'relative min-h-0 flex-1 overflow-hidden bg-bg',
            fileOver && 'outline-2 -outline-offset-2 outline-focus outline-dashed',
          )}
          onPointerMove={(e) => {
            pointer.current = { x: e.clientX, y: e.clientY };
          }}
          onPointerLeave={() => {
            pointer.current = null;
          }}
          onPointerDownCapture={() => {
            pointerDown.current = true;
          }}
          onPointerUpCapture={() => {
            pointerDown.current = false;
          }}
          onFocusCapture={(e) => {
            const id = (e.target as HTMLElement)
              .closest('[data-step-card]')
              ?.getAttribute('data-node');
            if (!id || e.target !== e.target.closest('[data-step-card]')) return;
            setLastFocus(id);
            const store = useEditorStore.getState();
            if (!pointerDown.current && !quietFocus.current && !store.selection.ids.includes(id))
              store.select([id]);
          }}
          onDragOver={(e) => {
            if (!e.dataTransfer.types.includes(STEP_MIME)) return;
            e.preventDefault();
            e.dataTransfer.dropEffect = 'copy';
            setFileOver(true);
          }}
          onDragLeave={(e) => {
            if (!e.currentTarget.contains(e.relatedTarget as Node | null)) setFileOver(false);
          }}
          onDrop={onDrop}
        >
          <ReactFlow<StepFlowNode, LinkFlowEdge>
            nodes={rfNodes}
            edges={rfEdges}
            nodeTypes={NODE_TYPES}
            edgeTypes={EDGE_TYPES}
            onNodesChange={onNodesChange}
            onEdgesChange={onEdgesChange}
            onNodeDragStart={() => setMovingCards(true)}
            onNodeDragStop={commitDrag}
            onConnectStart={link.onConnectStart}
            onConnectEnd={link.onConnectEnd}
            connectionLineComponent={DraftLink}
            connectionRadius={24}
            isValidConnection={() => false}
            onMoveStart={(e) => {
              if (e) vp.userMoved();
            }}
            onPaneClick={() => useEditorStore.getState().select([])}
            onPaneContextMenu={(e) => {
              e.preventDefault();
              openMenuAt(e.clientX, e.clientY, { kind: 'canvas', canPaste: true });
            }}
            onNodeContextMenu={(e, n) => {
              e.preventDefault();
              const store = useEditorStore.getState();
              if (!store.selection.ids.includes(n.id)) store.select([n.id]);
              const ids = useEditorStore.getState().selection.ids;
              openMenuAt(e.clientX, e.clientY, { kind: 'step', ids });
            }}
            onEdgeContextMenu={(e, edge) => {
              e.preventDefault();
              useEditorStore.getState().select([], edge.id);
              openMenuAt(e.clientX, e.clientY, { kind: 'link', edgeId: edge.id });
            }}
            onDoubleClick={(e) => {
              if (!(e.target as HTMLElement).classList.contains('react-flow__pane')) return;
              quickAdd({
                x: e.clientX,
                y: e.clientY,
                at: rf.screenToFlowPosition({ x: e.clientX, y: e.clientY }),
              });
            }}
            minZoom={ZOOM_MIN}
            maxZoom={ZOOM_MAX}
            zoomOnDoubleClick={false}
            zoomOnScroll={false}
            panOnScroll
            zoomActivationKeyCode={['Meta', 'Control']}
            panActivationKeyCode="Space"
            panOnDrag={tool === 'pan' ? [0, 1] : [1]}
            selectionOnDrag={tool === 'select'}
            selectionKeyCode="Shift"
            multiSelectionKeyCode={['Shift', 'Meta', 'Control']}
            nodesDraggable={tool === 'select'}
            nodesConnectable={tool === 'select'}
            elementsSelectable={tool === 'select'}
            nodesFocusable={false}
            edgesFocusable={false}
            disableKeyboardA11y
            deleteKeyCode={null}
            snapToGrid
            snapGrid={[8, 8]}
            elevateNodesOnSelect={false}
            elevateEdgesOnSelect={false}
            zIndexMode="manual"
            proOptions={{ hideAttribution: true }}
            aria-label={t.canvas.label}
          />
          {nodes.length === 0 && registry.loaded ? (
            <EmptyCanvas
              onAdd={(e) => {
                const r = e.currentTarget.getBoundingClientRect();
                quickAdd({ x: r.left + r.width / 2 - 170, y: r.bottom + 8, at: vp.centrePoint() });
              }}
            />
          ) : null}
          <StepToolbar
            ids={selection.ids}
            places={places}
            area={vp.size}
            hidden={tool !== 'select' || movingCards || !!link.dragging || !!menu}
          />
          <ZoomPill
            onZoom={vp.zoomBy}
            onReset={() => vp.zoomTo(1)}
            onFit={vp.fit}
            minimap={vp.minimap}
            onMinimap={vp.setMinimap}
          />
          <ToolBar
            tool={tool}
            onTool={setTool}
            anyExpanded={anyExpanded}
            onToggleAll={() =>
              setCollapsed(anyExpanded ? new Set(nodes.map((n) => n.id)) : new Set())
            }
            canUndo={canUndo}
            canRedo={canRedo}
            onUndo={ops.undo}
            onRedo={ops.redo}
            narrow={narrow}
          />
          {vp.minimap && !narrow ? (
            <Minimap places={places} selected={selected} area={vp.size} />
          ) : null}
        </div>
        {link.hint ? <HintBubble hint={link.hint} /> : null}
        {menu ? (
          <CanvasMenu request={menu} onAction={onMenu} onClose={() => setMenu(null)} />
        ) : null}
      </LinkToolsContext.Provider>
    </CanvasViewContext.Provider>
  );
}

/** The Editor's canvas: step cards, links, and the floating controls over them. */
export function EditorCanvas(props: CanvasProps) {
  return (
    <ReactFlowProvider>
      <Canvas {...props} />
    </ReactFlowProvider>
  );
}
