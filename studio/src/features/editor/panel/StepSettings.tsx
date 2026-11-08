import { Plus } from 'lucide-react';
import { Fragment, useEffect, useRef, useState, type ReactNode } from 'react';

import type { JsonObject, Node, NodeMetadata } from '@/api/types';
import { Button } from '@/components/ui/button';
import { useCopy } from '@/lib/copy';
import { useEditor } from '@/features/editor/editor-context';
import { useEditorStore } from '@/features/editor/editor-store';
import { issuesFor } from '@/features/editor/model/checks';
import {
  imageToolsOf,
  reviewToolsOf,
  specFor,
  type SectionId,
} from '@/features/editor/model/fields';
import { RunPreview } from './RunPreview';
import { Disclosure, Section } from './Section';
import { Connections, HowItRuns, SettingField } from './StepSections';
import { ToolPicker } from './ToolsField';
import { ReportResult } from '@/components/values/ReportResult';
import { hasReport, lastReport } from '@/lib/agent-report';
import { agentToolFlow } from '@/lib/agent-tools';
import { AgentRules, HowItWorks } from './AgentGuide';

// Instructions, tools and limits fold into Advanced on an Agent whose tools are all registered.
const DISCLOSURES = ['formats', 'behavior', 'api', 'instructions', 'tools', 'limits'] as const;
type DisclosureId = (typeof DISCLOSURES)[number];

/** The settings that say what a run must give an input step, in the order they are read. */
const INPUT_FIELDS: Record<string, readonly string[]> = {
  text_input: ['required', 'textLength'],
  task_input: ['schema', 'required'],
  image_input: ['required', 'acceptedMediaTypes', 'maxSizeBytes'],
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
  const { registry, issues, focusRequest, lastRun } = useEditor();
  const doc = useEditorStore((st) => st.doc);
  const [opened, setOpened] = useState<Partial<Record<DisclosureId, boolean>>>({});
  // Null on mount, so a request that selected this step (an issue, a link from a run) also
  // opens its disclosure: the focus below runs on mount for the same request.
  const [seenSeq, setSeenSeq] = useState<number | null>(null);
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
  // Sections that follow Data flow.
  const tail: ReactNode[] = [];
  const c = node.config;
  const tools = toolNames(node);
  const modelSection = (hint?: ReactNode) => {
    const showImage =
      node.type === 'agent' &&
      (imageToolsOf(node, registry.tools).length > 0 || c.imageModelId !== undefined);
    const showReview =
      node.type === 'agent' &&
      (reviewToolsOf(node, registry.tools).length > 0 || c.reviewModelId !== undefined);
    return section(
      'model',
      [
        field('modelId'),
        showImage ? field('imageModelId') : null,
        showReview ? field('reviewModelId') : null,
      ],
      hint ? { hint, title: s.models } : undefined,
    );
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
      const guide = agentToolFlow(node, registry.tools);
      if (guide) {
        const limitFields = ['maxTurns', 'timeoutMs', 'maxGenerationCalls'];
        const minutes = typeof c.timeoutMs === 'number' ? Math.round(c.timeoutMs / 60000) : null;
        parts.push(
          section('how', <HowItWorks steps={guide} />),
          section('rules', <AgentRules steps={guide} config={c} />),
          modelSection(
            tools.some((x) => registry.tools.get(x)?.requiresReviewModel)
              ? copy.editor.guide.reviewNote
              : undefined,
          ),
        );
        tail.push(
          <div key="advanced" className="px-5 pt-4 pb-1">
            <h3 className="m-0 text-sm font-semibold text-fg">{s.advanced}</h3>
          </div>,
          <Disclosure
            key="instructions"
            id="instructions"
            title={s.instructions}
            open={isOpen('instructions', ['instructions'])}
            onOpen={setOpen('instructions')}
          >
            {field('instructions')}
          </Disclosure>,
          <Disclosure
            key="tools"
            id="tools"
            title={s.toolDetails}
            meta={t.toolDetailsMeta(tools.length)}
            open={isOpen('tools', ['allowedTools'])}
            onOpen={setOpen('tools')}
          >
            <div className="mb-3 flex justify-end">
              <Button
                variant="secondary"
                size="sm"
                data-open-picker
                onClick={() => setPicker(true)}
              >
                <Plus aria-hidden="true" />
                {copy.editor.fields.addTool}
              </Button>
            </div>
            {field('allowedTools')}
          </Disclosure>,
          <Disclosure
            key="limits"
            id="limits"
            title={s.limits}
            meta={[
              typeof c.maxTurns === 'number' ? copy.editor.guide.turns(c.maxTurns) : null,
              minutes !== null ? copy.editor.guide.minutes(minutes) : null,
            ]
              .filter(Boolean)
              .join(', ')}
            open={isOpen('limits', limitFields)}
            onOpen={setOpen('limits')}
          >
            <p className="m-0 mb-3 text-sm text-fg-muted">{t.limitsHint}</p>
            {field('maxTurns')}
            {field('timeoutMs')}
            {counts || c.maxGenerationCalls !== undefined ? field('maxGenerationCalls') : null}
          </Disclosure>,
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
    case 'task_input':
    case 'image_input':
    case 'media_brief': {
      // What a run needs, how it looks in the run form, then the names the API and the form
      // use, which most people never change.
      const brief = node.type === 'media_brief';
      parts.push(
        section(
          'needs',
          brief ? (
            <p className="m-0 text-sm text-fg-muted">{copy.editor.fields.briefHint}</p>
          ) : (
            (INPUT_FIELDS[node.type] ?? []).map(field)
          ),
        ),
        section('runField', <RunPreview node={node} />, { hint: t.runFieldHint }),
        <Disclosure
          key="api"
          id="api"
          title={s.api}
          meta={
            typeof c.inputKey === 'string' ? <span className="font-mono">{c.inputKey}</span> : null
          }
          open={isOpen('api', ['label', 'inputKey'])}
          onOpen={setOpen('api')}
        >
          {brief ? (
            <p className="m-0 mb-3 text-sm text-fg-muted">{copy.editor.fields.briefLabelHint}</p>
          ) : (
            field('label')
          )}
          {field('inputKey')}
        </Disclosure>,
      );
      break;
    }
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
        // What this output showed in the last run, through its Agent's report format.
        const report = lastReport(doc.nodes, doc.edges, node, lastRun.byNode.get(node.id));
        if (hasReport(report)) parts.push(section('lastResult', <ReportResult report={report} />));
      } else {
        const fields = [...meta.uiSchema.fields]
          .sort((a, b) => a.order - b.order)
          .map((x) => x.path);
        parts.push(section('settings', fields.map(field)));
      }
  }
  parts.push(<Connections key="connections" node={node} meta={meta} />, ...tail);

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
