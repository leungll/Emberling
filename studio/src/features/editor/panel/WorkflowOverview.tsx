import { Pencil, Plus, TriangleAlert } from 'lucide-react';
import { useMemo, useRef, useState } from 'react';

import { Button, IconButton } from '@/components/ui/button';
import { GrowArea, controlClasses } from '@/components/ui/field';
import { WorkflowSummary } from '@/components/workflow/WorkflowSummary';
import { useCopy } from '@/lib/copy';
import { cn } from '@/lib/utils';
import { useEditor } from '@/features/editor/editor-context';
import { useEditorStore } from '@/features/editor/editor-store';
import { IssueList } from '@/features/editor/IssueList';
import { Section } from './Section';

/** The description, read as text, edited in place on request. */
function Description() {
  const copy = useCopy();
  const t = copy.editor.panel;
  const description = useEditorStore((s) => s.doc.description);
  const [draft, setDraft] = useState<string | null>(null);
  const area = useRef<HTMLTextAreaElement>(null);
  const editRef = useRef<HTMLDivElement>(null);
  const start = () => {
    setDraft(description);
    requestAnimationFrame(() => {
      const ta = area.current;
      if (!ta) return;
      ta.focus();
      ta.setSelectionRange(ta.value.length, ta.value.length);
    });
  };
  const finish = (save: boolean) => {
    if (save && draft !== null && draft.trim() !== description) {
      const s = useEditorStore.getState();
      s.begin();
      s.setDescription(draft.trim());
    }
    setDraft(null);
    requestAnimationFrame(() => editRef.current?.querySelector('button')?.focus());
  };

  return (
    <Section
      id="does"
      title={t.whatItDoes}
      actions={
        draft === null && description ? (
          <div ref={editRef}>
            <IconButton label={t.editDescription} tooltip={t.editDescription} onClick={start}>
              <Pencil aria-hidden="true" />
            </IconButton>
          </div>
        ) : null
      }
    >
      {draft !== null ? (
        <div className="flex flex-col gap-2">
          <GrowArea
            ref={area}
            aria-label={t.descriptionLabel}
            placeholder={t.descriptionPlaceholder}
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Escape') {
                e.stopPropagation();
                finish(false);
              }
              if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
                e.preventDefault();
                finish(true);
              }
            }}
            className={cn(controlClasses, 'text-sm')}
          />
          <p className="m-0 text-sm text-fg-muted">{t.descriptionHint}</p>
          <div className="flex justify-end gap-2">
            <Button variant="ghost" size="sm" onClick={() => finish(false)}>
              {t.cancel}
            </Button>
            <Button variant="secondary" size="sm" onClick={() => finish(true)}>
              {t.done}
            </Button>
          </div>
        </div>
      ) : description ? (
        <p className="m-0 text-sm whitespace-pre-wrap text-fg">{description}</p>
      ) : (
        <div ref={editRef} className="flex flex-col items-start gap-1">
          <Button variant="ghost" size="sm" onClick={start} className="-ml-2">
            <Plus aria-hidden="true" />
            {t.addDescription}
          </Button>
          <p className="m-0 text-sm text-fg-muted">{t.descriptionHint}</p>
        </div>
      )}
    </Section>
  );
}

/**
 * The side panel with nothing selected: the workflow's issues, what it does, its steps,
 * inputs and outputs. The last run lives in the strip under the canvas.
 */
export function WorkflowOverview() {
  const copy = useCopy();
  const { registry, issues, openSettings, showIssue } = useEditor();
  const nodes = useEditorStore((s) => s.doc.nodes);
  const edges = useEditorStore((s) => s.doc.edges);
  const description = useEditorStore((s) => s.doc.description);
  const definition = useMemo(() => ({ nodes, edges, description }), [nodes, edges, description]);
  const summaryRegistry = useMemo(
    () => ({ nodeTypes: registry.catalog, models: registry.models }),
    [registry.catalog, registry.models],
  );
  return (
    <div>
      {issues.length ? (
        <Section id="issues" title={copy.editor.checks.title(issues.length)}>
          <IssueList issues={issues} nodes={nodes} catalog={registry.catalog} onShow={showIssue} />
          <p className="m-0 mt-2 text-sm text-fg-muted">{copy.editor.checks.foot}</p>
        </Section>
      ) : null}
      <Description />
      <section data-section="summary" className="border-b border-border-subtle px-5 py-4">
        <WorkflowSummary
          definition={definition}
          registry={summaryRegistry}
          hideDescription
          showEmptyOutputs
          onStepClick={(id) => openSettings(id)}
          onIoClick={(id) => openSettings(id)}
          stepExtra={(id) => {
            const n = issues.filter((i) => i.node === id).length;
            return n ? (
              <span className="inline-flex items-center gap-1 text-sm text-caution-fg">
                <TriangleAlert aria-hidden="true" className="size-icon-sm" />
                {copy.editor.header.issues(n)}
              </span>
            ) : null;
          }}
        />
      </section>
    </div>
  );
}
