import { Plus } from 'lucide-react';
import { Fragment, useEffect, useRef, useState, type ReactNode } from 'react';

import type { JsonObject, Node, NodeMetadata } from '@/api/types';
import { Button } from '@/components/ui/button';
import { useCopy } from '@/lib/copy';
import { useEditor } from '@/features/editor/editor-context';
import { issuesFor } from '@/features/editor/model/checks';
import { imageToolsOf, specFor, type SectionId } from '@/features/editor/model/fields';
import { RunPreview } from './RunPreview';
import { Disclosure, Section } from './Section';
import { Connections, HowItRuns, SettingField } from './StepSections';
import { ToolPicker } from './ToolsField';

const DISCLOSURES = ['formats', 'behavior', 'api'] as const;
type DisclosureId = (typeof DISCLOSURES)[number];

const INPUT_FIELDS: Record<string, readonly string[]> = {
  text_input: ['label', 'required', 'textLength'],
  image_input: ['label', 'required', 'acceptedMediaTypes', 'maxSizeBytes'],
  media_brief: [],
};
const OUTPUT_TYPES = ['text_output', 'media_output', 'media_result'];

function toolNames(node: Node): string[] {
  const v = node.config.allowedTools;
  return Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : [];
}

/**
 * A step's settings, in the sections its type needs. A focus request (from a card row, an
 * issue or "All settings") opens the section that holds the field, scrolls to it, focuses its
 * control and flashes it once.
 */
export function StepSettings({ node, meta }: { node: Node; meta: NodeMetadata }) {
  const copy = useCopy();
  const t = copy.editor.panel;
  const s = t.sections;
  const { registry, issues, focusRequest } = useEditor();
  const [opened, setOpened] = useState<Partial<Record<DisclosureId, boolean>>>({});
  const [seenSeq, setSeenSeq] = useState(focusRequest?.seq ?? 0);
  const [picker, setPicker] = useState(false);
  const body = useRef<HTMLDivElement>(null);

  // A request for a field inside a closed disclosure opens it before the field is focused.
  const req = focusRequest?.nodeId === node.id ? focusRequest : null;
  if (req && req.seq !== seenSeq) {
    setSeenSeq(req.seq);
    const section = req.field ? specFor(node.type, req.field)?.section : undefined;
    if (section && (DISCLOSURES as readonly string[]).includes(section)) {
      setOpened((o) => ({ ...o, [section]: true }));
    }
  }

  const reqSeq = req?.seq;
  const reqField = req?.field ?? null;
  useEffect(() => {
    if (reqSeq === undefined || !body.current) return;
    const root = body.current;
    const target = reqField
      ? root.querySelector<HTMLElement>(`[data-field="${CSS.escape(reqField)}"]`)
      : null;
    const el = target ?? root.querySelector<HTMLElement>('[data-section]');
    if (!el) return;
    el.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
    if (!target) return;
    const control = target.querySelector<HTMLElement>('input, textarea, button, [tabindex="0"]');
    control?.focus({ preventScroll: true });
    target.classList.add('bg-caution-bg', 'transition-colors', 'duration-2');
    const timer = window.setTimeout(() => target.classList.remove('bg-caution-bg'), 1200);
    return () => window.clearTimeout(timer);
  }, [reqSeq, reqField]);

  const hasIssues = (fields: readonly string[]) =>
    fields.some((f) => issuesFor(issues, node.id, f).length > 0);
  const isOpen = (id: DisclosureId, fields: readonly string[]) => opened[id] ?? hasIssues(fields);
  const setOpen = (id: DisclosureId) => (v: boolean) => setOpened((o) => ({ ...o, [id]: v }));
  const field = (key: string) => <SettingField key={key} node={node} field={key} />;
  const section = (
    id: SectionId,
    children: ReactNode,
    extra?: Partial<Parameters<typeof Section>[0]>,
  ) => (
    <Section id={id} title={s[id]} {...extra}>
      {children}
    </Section>
  );

  const parts: ReactNode[] = [];
  const c = node.config;
  const tools = toolNames(node);
  const modelSection = () => {
    const showImage =
      node.type === 'agent' &&
      (imageToolsOf(node, registry.tools).length > 0 || c.imageModelId !== undefined);
    return section('model', [field('modelId'), showImage ? field('imageModelId') : null]);
  };
  const behaviorSection = () => {
    const model = typeof c.modelId === 'string' ? registry.models.get(c.modelId) : undefined;
    const defaults =
      (model?.configSchema as { properties?: Record<string, { default?: unknown }> } | undefined)
        ?.properties ?? {};
    const mc: JsonObject =
      c.modelConfig && typeof c.modelConfig === 'object' && !Array.isArray(c.modelConfig)
        ? c.modelConfig
        : {};
    const changed = Object.keys(mc).filter(
      (k) => !defaults[k] || mc[k] !== defaults[k].default,
    ).length;
    return (
      <Disclosure
        key="behavior"
        id="behavior"
        title={s.behavior}
        meta={t.behaviorMeta(changed)}
        open={isOpen('behavior', ['modelConfig'])}
        onOpen={setOpen('behavior')}
      >
        {field('modelConfig')}
      </Disclosure>
    );
  };

  switch (node.type) {
    case 'agent': {
      const counts = tools.some((x) => registry.tools.get(x)?.countsTowardGenerationLimit);
      const formats = ['contextSchema', 'stateSchema', 'outputSchema'];
      parts.push(
        section('instructions', field('instructions')),
        modelSection(),
        section('tools', field('allowedTools'), {
          count: tools.length || null,
          actions: (
            <Button variant="secondary" size="sm" data-open-picker onClick={() => setPicker(true)}>
              <Plus aria-hidden="true" />
              {copy.editor.fields.addTool}
            </Button>
          ),
        }),
        section(
          'limits',
          [
            field('maxTurns'),
            field('timeoutMs'),
            counts || c.maxGenerationCalls !== undefined ? field('maxGenerationCalls') : null,
          ],
          { hint: t.limitsHint },
        ),
        <Disclosure
          key="formats"
          id="formats"
          title={s.formats}
          optional
          meta={t.formatsMeta(formats.filter((k) => c[k] !== undefined).length)}
          open={isOpen('formats', formats)}
          onOpen={setOpen('formats')}
        >
          <p className="m-0 mb-3 text-sm text-fg-muted">{t.formatsHint}</p>
          {formats.map(field)}
        </Disclosure>,
        behaviorSection(),
      );
      break;
    }
    case 'text_generation':
      parts.push(
        modelSection(),
        section('systemPrompt', field('systemPrompt'), { optional: true }),
        behaviorSection(),
        <HowItRuns key="runs" node={node} meta={meta} />,
      );
      break;
    case 'image_generation':
      parts.push(
        modelSection(),
        section('size', field('width')),
        behaviorSection(),
        <HowItRuns key="runs" node={node} meta={meta} />,
      );
      break;
    case 'prompt_template':
      parts.push(section('template', field('template')));
      break;
    case 'text_input':
    case 'image_input':
    case 'media_brief':
      parts.push(
        section(
          'runField',
          <>
            <RunPreview node={node} />
            {(INPUT_FIELDS[node.type] ?? []).map(field)}
            {node.type === 'media_brief' ? (
              <p className="m-0 text-sm text-fg-muted">{copy.editor.fields.briefHint}</p>
            ) : null}
          </>,
          { hint: t.runFieldHint },
        ),
        <Disclosure
          key="api"
          id="api"
          title={s.api}
          meta={
            typeof c.inputKey === 'string' ? <span className="font-mono">{c.inputKey}</span> : null
          }
          open={isOpen('api', ['inputKey'])}
          onOpen={setOpen('api')}
        >
          {field('inputKey')}
        </Disclosure>,
      );
      break;
    default:
      if (OUTPUT_TYPES.includes(node.type) || !meta.uiSchema.fields.length) {
        parts.push(
          section(
            'settings',
            <p className="m-0 text-sm text-fg-muted">
              {t.noSettings[node.type] ?? t.noSettingsOther}
            </p>,
          ),
        );
      } else {
        const fields = [...meta.uiSchema.fields]
          .sort((a, b) => a.order - b.order)
          .map((x) => x.path);
        parts.push(section('settings', fields.map(field)));
      }
  }
  parts.push(<Connections key="connections" node={node} meta={meta} />);

  return (
    <div ref={body}>
      {parts.map((p, i) => (
        <Fragment key={i}>{p}</Fragment>
      ))}
      {node.type === 'agent' ? (
        <ToolPicker
          nodeId={node.id}
          tools={registry.tools}
          open={picker}
          onClose={() => setPicker(false)}
        />
      ) : null}
    </div>
  );
}
