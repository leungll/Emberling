import { TriangleAlert } from 'lucide-react';

import type { Node, NodeMetadata } from '@/api/types';
import { FactList, type Fact } from '@/components/facts/FactList';
import { Button } from '@/components/ui/button';
import { useCopy } from '@/lib/copy';
import { duration } from '@/lib/time';
import { useEditor } from '@/features/editor/editor-context';
import { useEditorStore } from '@/features/editor/editor-store';
import { issuesFor } from '@/features/editor/model/checks';
import { imageToolsOf, specFor } from '@/features/editor/model/fields';
import { portLabel, sourcesOf, targetsOf } from '@/features/editor/model/steps';
import {
  FormatsField,
  GenericField,
  JsonField,
  KeyField,
  LabelField,
  LengthField,
  LongTextField,
  NumberField,
  PromptField,
  RequiredField,
} from './BasicFields';
import { ModelConfigField, ModelField } from './ModelFields';
import { SchemaSlot } from './SchemaSlot';
import { Section } from './Section';
import { ToolsField } from './ToolsField';

/** One setting of a step, drawn by the control its spec names or, without one, by its schema type. */
export function SettingField({ node, field }: { node: Node; field: string }) {
  const copy = useCopy();
  const { registry, issues } = useEditor();
  const meta = registry.catalog.get(node.type);
  const spec = specFor(node.type, field);
  const errors = issuesFor(issues, node.id, field)
    .filter((i) => field !== 'modelConfig' || i.field === 'modelConfig')
    .map((i) => i.text);
  const props = { node, field, errors };
  switch (spec?.widget) {
    case 'longtext':
      return <LongTextField {...props} />;
    case 'model': {
      const capability = meta?.uiSchema.fields.find((x) => x.path === field)?.capability;
      const hint =
        field === 'imageModelId'
          ? copy.editor.fields.imageModelHint(
              imageToolsOf(node, registry.tools)
                .map((x) => x.name)
                .join(', '),
            )
          : undefined;
      return <ModelField {...props} models={registry.models} capability={capability} hint={hint} />;
    }
    case 'tools':
      return <ToolsField node={node} tools={registry.tools} errors={errors} />;
    case 'int': {
      const counted = [...registry.tools.values()]
        .filter((x) => x.countsTowardGenerationLimit && toolNames(node).includes(x.name))
        .map((x) => x.name);
      const hint =
        field === 'maxGenerationCalls' && counted.length
          ? copy.editor.fields.generationHint(counted.join(', '))
          : undefined;
      return <NumberField {...props} hint={hint} />;
    }
    case 'minutes':
      return <NumberField {...props} unitKind="minutes" />;
    case 'sizeMB':
      return <NumberField {...props} unitKind="mb" />;
    case 'schema':
      return <SchemaSlot {...props} />;
    case 'modelConfig':
      return (
        <ModelConfigField
          {...props}
          issues={issuesFor(issues, node.id, 'modelConfig')}
          models={registry.models}
        />
      );
    case 'prompt':
      return <PromptField {...props} inputs={(meta?.inputs ?? []).map((p) => p.name)} />;
    case 'label':
      return <LabelField {...props} />;
    case 'required':
      return <RequiredField {...props} />;
    case 'lengthRange':
      return <LengthField {...props} />;
    case 'formats':
      return <FormatsField {...props} />;
    case 'key':
      return <KeyField {...props} />;
    case undefined: {
      const schema = schemaOf(meta, field);
      if (schema.type === 'object' || schema.type === 'array') {
        return <JsonField {...props} label={field} />;
      }
      return <GenericField {...props} schema={schema} />;
    }
  }
}

function toolNames(node: Node): string[] {
  const v = node.config.allowedTools;
  return Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : [];
}

function schemaOf(meta: NodeMetadata | undefined, field: string): Record<string, unknown> {
  const props = (
    meta?.configSchema as { properties?: Record<string, Record<string, unknown>> } | undefined
  )?.properties;
  return props?.[field] ?? {};
}

/** How the step runs, listing only what differs from the safe default: no outside effect, answers at once. */
export function HowItRuns({ node, meta }: { node: Node; meta: NodeMetadata }) {
  const copy = useCopy();
  const h = copy.editor.fields.howItRuns;
  const se = meta.sideEffect;
  const p = node.executionPolicy;
  const keyed = se.idempotency === 'KEYED';
  const facts: (Fact | null)[] = [
    meta.executionKind === 'ASYNC' ? { label: h.result, value: h.resultLater } : null,
    se.kind === 'EXTERNAL'
      ? {
          label: h.effect,
          value: (
            <span className="inline-flex items-start gap-1 text-caution-fg">
              <TriangleAlert aria-hidden="true" className="mt-0.5 size-icon-sm shrink-0" />
              {keyed ? h.effectKeyed : h.effectUnsafe}
            </span>
          ),
        }
      : null,
    p && p.maxAttempts > 0
      ? {
          label: h.retries,
          value: p.maxAttempts === 1 ? h.noRetries : h.upToTries(p.maxAttempts, keyed),
        }
      : null,
    p && p.timeoutMs > 0
      ? {
          label: h.timeLimit,
          value: p.maxAttempts > 1 ? h.perTry(duration(p.timeoutMs)) : duration(p.timeoutMs),
        }
      : null,
  ];
  if (!facts.some(Boolean)) return null;
  return (
    <Section id="howItRuns" title={copy.editor.panel.sections.howItRuns}>
      <FactList facts={facts} compact />
    </Section>
  );
}

/** Where each input comes from and where each output goes; names open those steps. */
export function Connections({ node, meta }: { node: Node; meta: NodeMetadata }) {
  const copy = useCopy();
  const t = copy.editor;
  const { openSettings, issues } = useEditor();
  const nodes = useEditorStore((s) => s.doc.nodes);
  const edges = useEditorStore((s) => s.doc.edges);
  if (!meta.inputs.length && !meta.outputs.length) return null;
  const links = (list: Node[], word: string) => (
    <span className="inline-flex flex-wrap items-baseline gap-x-1">
      <span className="text-fg-muted">{word}</span>
      {list.map((s, i) => (
        <span key={s.id}>
          <Button
            variant="link"
            size="sm"
            onClick={() => openSettings(s.id)}
            className="min-h-control-xs"
          >
            {s.name}
          </Button>
          {i < list.length - 1 ? ',' : null}
        </span>
      ))}
    </span>
  );
  const facts: Fact[] = [
    ...meta.inputs.map((p) => {
      const from = sourcesOf(nodes, edges, node.id, p.name);
      return {
        label: portLabel(p),
        value: from.length ? (
          links(from, t.panel.connFrom)
        ) : p.required ? (
          <span className="inline-flex items-center gap-1 text-danger-fg">
            <TriangleAlert aria-hidden="true" className="size-icon-sm" />
            {t.canvas.notConnected}
          </span>
        ) : (
          <span className="text-fg-muted">{t.canvas.notConnectedOptional}</span>
        ),
      };
    }),
    ...meta.outputs.map((p) => {
      const to = targetsOf(nodes, edges, node.id, p.name);
      return {
        label: t.panel.connOut(portLabel(p)),
        value: to.length ? (
          links(to, t.panel.connTo)
        ) : (
          <span className="text-fg-muted">{t.panel.notUsed}</span>
        ),
      };
    }),
  ];
  const errors = issuesFor(issues, node.id).filter((i) => i.section === 'connections');
  return (
    <Section id="connections" title={t.panel.sections.connections} hint={t.panel.connectionsHint}>
      <div data-field="connections">
        <FactList facts={facts} compact />
        {errors.map((e) => (
          <p key={e.text} className="m-0 mt-1 text-sm text-danger-fg">
            {e.text}
          </p>
        ))}
      </div>
    </Section>
  );
}
