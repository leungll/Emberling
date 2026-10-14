import { TriangleAlert } from 'lucide-react';
import type { ReactNode } from 'react';

import type { Node, NodeMetadata, PortMetadata } from '@/api/types';
import { useCopy } from '@/lib/copy';
import { bytes } from '@/lib/time';
import { cn } from '@/lib/utils';
import { agentLimits } from '@/lib/workflow-summary';
import { useEditor } from '@/features/editor/editor-context';
import { useEditorStore } from '@/features/editor/editor-store';
import { linkProblem } from '@/features/editor/model/connect';
import { formatNames } from '@/features/editor/model/fields';
import { fieldLabel, portLabel, sourcesOf } from '@/features/editor/model/steps';
import { CardMedia } from './CardMedia';
import { useCanvasView } from './canvas-view';
import { InPort, type PortHint } from './Ports';

const ROW_COLS = { gridTemplateColumns: '72px minmax(0, 1fr)' };

/** A clickable row that opens the matching field in the side panel. */
function Row({
  nodeId,
  field,
  label,
  children,
  top = false,
  port,
}: {
  nodeId: string;
  field: string | null;
  label?: string;
  children: ReactNode;
  top?: boolean;
  port?: ReactNode;
}) {
  const { openSettings } = useEditor();
  const body = label ? (
    <>
      <div className="min-w-0 wrap-anywhere text-fg-muted">{label}</div>
      <div className="min-w-0 wrap-anywhere text-fg-secondary">{children}</div>
    </>
  ) : (
    children
  );
  return (
    <div
      data-node-field={field ?? undefined}
      style={label ? ROW_COLS : undefined}
      onClick={(e) => {
        if (!field) return;
        e.stopPropagation();
        openSettings(nodeId, field);
      }}
      className={cn(
        'relative px-3 py-1 text-sm',
        label
          ? cn('grid gap-2', top ? 'items-start' : 'items-baseline')
          : 'wrap-anywhere text-fg-secondary',
        field && 'cursor-pointer hover:bg-surface-hover',
      )}
    >
      {port}
      {body}
    </div>
  );
}

function Muted({ children }: { children: ReactNode }) {
  return <span className="text-fg-muted">{children}</span>;
}

function Missing({ children }: { children: ReactNode }) {
  return (
    <span className="inline-flex items-center gap-1 text-fg">
      <TriangleAlert aria-hidden="true" className="size-icon-xs text-caution-fg" />
      {children}
    </span>
  );
}

/** The incoming text of a template with its {{variables}} marked. */
function TemplateLine({ text }: { text: string }) {
  const parts = text.split(/(\{\{\s*[\w.]+\s*\}\})/g);
  return (
    <span
      data-ui-exempt="overflow:template-preview-full-text-in-panel"
      className="line-clamp-2 text-fg-secondary"
    >
      {parts.map((p, i) =>
        p.startsWith('{{') ? (
          <span key={i} className="rounded-sm bg-surface-selected font-mono text-accent-fg">
            {p}
          </span>
        ) : (
          p
        ),
      )}
    </span>
  );
}

/**
 * The read-only summary rows of a full card. Each row opens its field in the side panel;
 * an input row carries its port, so a link enters at the row it feeds.
 */
export function CardBody({ node, meta }: { node: Node; meta: NodeMetadata }) {
  const copy = useCopy();
  const t = copy.editor;
  const { registry, lastRun } = useEditor();
  const view = useCanvasView();
  const doc = useEditorStore((s) => s.doc);
  const c = node.config;

  const hintFor = (p: PortMetadata): PortHint => {
    const d = view.dragging;
    if (!d) return null;
    const problem = linkProblem(doc, registry.catalog, d.sid, d.sh, node.id, p.name);
    if (!problem) return 'ok';
    return problem.code === 'busy' ? 'busy' : 'bad';
  };

  const portRow = (p: PortMetadata) => {
    const src = sourcesOf(doc.nodes, doc.edges, node.id, p.name);
    const value = src.length ? (
      <>
        {t.canvas.from} <span className="text-fg">{t.names(src.map((s) => s.name))}</span>
      </>
    ) : p.required ? (
      <Missing>{t.canvas.notConnected}</Missing>
    ) : (
      <Muted>{t.canvas.notConnectedOptional}</Muted>
    );
    return (
      <Row
        key={`in-${p.name}`}
        nodeId={node.id}
        field="connections"
        label={portLabel(p)}
        port={<InPort port={p} hint={hintFor(p)} />}
      >
        {value}
      </Row>
    );
  };

  const modelName = (key: string) => {
    const id = c[key];
    if (typeof id !== 'string' || !id) return <Muted>{t.canvas.notChosen}</Muted>;
    const m = registry.models.get(id);
    return m ? m.displayName : <Missing>{t.canvas.notAvailable(id)}</Missing>;
  };

  const rows: ReactNode[] = [];
  const ports = () => meta.inputs.forEach((p) => rows.push(portRow(p)));
  const asks = (
    <>
      {t.canvas.asksFor} <span className="font-medium text-fg">{fieldLabel(node)}</span>
      {c.required === true ? null : <Muted> {t.canvas.optional}</Muted>}
    </>
  );

  switch (node.type) {
    case 'agent': {
      ports();
      rows.push(
        <Row key="model" nodeId={node.id} field="modelId" label={t.canvas.rows.model}>
          {modelName('modelId')}
        </Row>,
      );
      const tools = Array.isArray(c.allowedTools)
        ? c.allowedTools.filter((x): x is string => typeof x === 'string')
        : [];
      rows.push(
        <Row key="tools" nodeId={node.id} field="allowedTools" label={t.canvas.rows.tools} top>
          {tools.length ? (
            <span className="flex flex-wrap gap-1">
              {tools.map((x) => (
                <span
                  key={x}
                  className="inline-flex h-control-xs items-center rounded-sm bg-surface-hover px-2 font-mono text-sm text-fg-secondary"
                >
                  {x}
                </span>
              ))}
            </span>
          ) : (
            <Muted>{t.canvas.none}</Muted>
          )}
        </Row>,
      );
      rows.push(
        <Row key="limits" nodeId={node.id} field="maxTurns" label={t.canvas.rows.limits}>
          {agentLimits(c, 'card')}
        </Row>,
      );
      break;
    }
    case 'text_generation':
      ports();
      rows.push(
        <Row key="model" nodeId={node.id} field="modelId" label={t.canvas.rows.model}>
          {modelName('modelId')}
        </Row>,
      );
      if (typeof c.systemPrompt === 'string' && c.systemPrompt) {
        rows.push(
          <Row key="sys" nodeId={node.id} field="systemPrompt">
            <Muted>{t.canvas.systemPromptSet}</Muted>
          </Row>,
        );
      }
      break;
    case 'image_generation':
      ports();
      rows.push(
        <Row key="model" nodeId={node.id} field="modelId" label={t.canvas.rows.model}>
          {modelName('modelId')}
        </Row>,
      );
      rows.push(
        <Row key="width" nodeId={node.id} field="width" label={t.canvas.rows.width}>
          {typeof c.width === 'number' ? t.canvas.px(c.width) : <Muted>{t.canvas.notSet}</Muted>}
        </Row>,
      );
      break;
    case 'prompt_template': {
      ports();
      const first = typeof c.template === 'string' ? (c.template.split('\n')[0] ?? '') : '';
      rows.push(
        <Row key="tpl" nodeId={node.id} field="template">
          {first ? <TemplateLine text={first} /> : <Muted>{t.canvas.noTemplate}</Muted>}
        </Row>,
      );
      break;
    }
    case 'text_input': {
      rows.push(
        <Row key="asks" nodeId={node.id} field="label">
          {asks}
        </Row>,
      );
      const lo = typeof c.minLength === 'number' ? c.minLength : undefined;
      const hi = typeof c.maxLength === 'number' ? c.maxLength : undefined;
      const length = t.fields.length(lo, hi);
      if (length) {
        rows.push(
          <Row key="len" nodeId={node.id} field="textLength">
            <Muted>{length}</Muted>
          </Row>,
        );
      }
      break;
    }
    case 'image_input':
      rows.push(
        <Row key="asks" nodeId={node.id} field="label">
          {asks}
        </Row>,
      );
      rows.push(
        <Row key="acc" nodeId={node.id} field="acceptedMediaTypes">
          <Muted>
            {t.fields.accepts(
              formatNames(c.acceptedMediaTypes),
              typeof c.maxSizeBytes === 'number' ? bytes(c.maxSizeBytes) : '',
            )}
          </Muted>
        </Row>,
      );
      break;
    case 'media_brief':
      ports();
      rows.push(
        <Row key="asks" nodeId={node.id} field="inputKey">
          {t.canvas.photoSet}
        </Row>,
      );
      break;
    default:
      ports();
  }

  const nr = lastRun.byNode.get(node.id);
  const preview = node.type === 'text_output' ? outputPreview(nr?.output) : null;

  return (
    <div className="flex flex-col border-t border-border-subtle py-1">
      {rows}
      <CardMedia node={node} />
      {preview ? (
        <Row nodeId={node.id} field={null}>
          {preview}
        </Row>
      ) : null}
    </div>
  );
}

/** The last run's text result: the keys of a JSON object, or the first lines of plain text. */
function outputPreview(output: unknown): ReactNode {
  const text =
    output &&
    typeof output === 'object' &&
    !Array.isArray(output) &&
    typeof (output as { text?: unknown }).text === 'string'
      ? (output as { text: string }).text
      : null;
  if (text === null) return null;
  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch {
    parsed = null;
  }
  if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
    return (
      <span className="font-mono wrap-anywhere">{`{ ${Object.keys(parsed).join(', ')} }`}</span>
    );
  }
  return (
    <span
      data-ui-exempt="overflow:result-preview-full-text-on-run-page"
      className="line-clamp-3 text-fg-secondary"
    >
      {text}
    </span>
  );
}
