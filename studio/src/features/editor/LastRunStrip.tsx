import { ArrowRight, ChevronDown, ChevronUp } from 'lucide-react';
import { useState } from 'react';
import { Link, useLocation } from 'react-router';

import type { Node } from '@/api/types';
import { StatusIndicator } from '@/components/status/StatusIndicator';
import { Duration, RelativeTime } from '@/components/time/Time';
import { useCopy } from '@/lib/copy';
import { carryFrom } from '@/lib/nav/list-state';
import { readString, writeString } from '@/lib/storage';
import { cn } from '@/lib/utils';
import { useEditor } from './editor-context';
import { useEditorStore } from './editor-store';
import { ordered } from './model/steps';

const OPEN_KEY = 'emb.editor.strip';

const linkClass =
  'inline-flex min-h-control-xs items-center gap-1 rounded-sm text-sm font-medium text-accent-fg hover:underline';

/**
 * The last run along the bottom of the Editor: its status, when it started and how long it
 * took; opened, each step of the saved version with how it went in that run.
 */
export function LastRunStrip({
  savedNodes,
  version,
}: {
  savedNodes: readonly Node[];
  version: number | null;
}) {
  const copy = useCopy();
  const t = copy.editor.strip;
  const location = useLocation();
  const { workflowId, lastRun, openSettings } = useEditor();
  const nodes = useEditorStore((s) => s.doc.nodes);
  const selected = useEditorStore((s) => s.selection.ids);
  const [open, setOpenState] = useState(() => readString('local', OPEN_KEY) === '1');
  const setOpen = (v: boolean) => {
    setOpenState(v);
    writeString('local', OPEN_KEY, v ? '1' : null);
  };

  const run = lastRun.snapshot?.run;
  const summary = lastRun.summary;
  if (!workflowId || !lastRun.loaded) return null;
  if (!summary || !run) {
    return (
      <div className="flex min-h-control-md shrink-0 items-center gap-3 border-t border-border-subtle bg-surface px-4 text-sm">
        <span className="font-medium text-fg-secondary">{t.label}</span>
        <span className="text-fg-muted">{t.notRun}</span>
      </div>
    );
  }

  const runHref = carryFrom(
    `/workflows/${encodeURIComponent(workflowId)}/runs/${encodeURIComponent(run.id)}`,
    location.search,
  );
  const tookMs =
    run.startedAt && run.completedAt
      ? new Date(run.completedAt).getTime() - new Date(run.startedAt).getTime()
      : null;
  const current = new Set(nodes.map((n) => n.id));
  const failed = run.status === 'FAILED';

  return (
    <section aria-label={t.label} className="shrink-0 border-t border-border-subtle bg-surface">
      <div className="flex min-h-control-md flex-wrap items-center gap-x-4 gap-y-1 px-2 py-1 text-sm">
        <button
          type="button"
          aria-expanded={open}
          aria-label={open ? t.hide : t.show}
          onClick={() => setOpen(!open)}
          className="inline-flex h-control-sm items-center gap-1.5 rounded-md px-2 font-medium text-fg-secondary hover:bg-surface-hover hover:text-fg"
        >
          {open ? (
            <ChevronDown aria-hidden="true" className="size-icon-sm" />
          ) : (
            <ChevronUp aria-hidden="true" className="size-icon-sm" />
          )}
          {t.label}
        </button>
        <StatusIndicator status={{ kind: 'run', value: run.status }} />
        <span className="inline-flex items-center gap-1 text-fg-muted">
          {t.started}
          <RelativeTime iso={run.startedAt ?? summary.createdAt} className="text-fg-secondary" />
        </span>
        {tookMs !== null ? (
          <span className="inline-flex items-center gap-1 text-fg-muted">
            {t.took}
            <Duration ms={tookMs} className="text-fg-secondary" />
          </span>
        ) : null}
        {version !== null && run.definitionVersion < version ? (
          <span className="text-fg-muted">{t.older(run.definitionVersion)}</span>
        ) : null}
        <span className="flex-1" />
        <Link
          to={carryFrom(`/workflows/${encodeURIComponent(workflowId)}/runs`, location.search)}
          className={cn(linkClass, 'text-fg-secondary')}
        >
          {t.allRuns}
        </Link>
        <Link to={runHref} className={cn(linkClass, 'pr-2')}>
          {t.openRun}
          <ArrowRight aria-hidden="true" className="size-icon-sm" />
        </Link>
      </div>
      {open ? (
        <ol
          style={{ maxHeight: 240 }}
          className="m-0 flex list-none flex-col overflow-y-auto border-t border-border-subtle p-0 py-1"
        >
          {ordered(savedNodes).map((n) => {
            const nr = lastRun.byNode.get(n.id);
            const removed = !current.has(n.id);
            return (
              <li
                key={n.id}
                className={cn(
                  'flex min-h-control-md items-center gap-3 px-4',
                  selected.includes(n.id) && 'bg-surface-selected',
                )}
              >
                <StatusIndicator
                  status={{
                    kind: 'step',
                    value: nr ? nr.status : failed ? 'NONE_RUN_FAILED' : 'NONE',
                  }}
                  size="icon"
                />
                {removed ? (
                  <span className="min-w-0 flex-1 text-sm wrap-anywhere text-fg-muted">
                    {n.name}
                    <span className="ml-2">{t.removed}</span>
                  </span>
                ) : (
                  <span className="min-w-0 flex-1">
                    <button
                      type="button"
                      onClick={() => openSettings(n.id)}
                      className="min-h-control-xs rounded-sm text-left text-sm font-medium wrap-anywhere text-fg hover:underline"
                    >
                      {n.name}
                    </button>
                  </span>
                )}
                {nr?.latencyMs !== null && nr?.latencyMs !== undefined ? (
                  <Duration ms={nr.latencyMs} className="text-sm text-fg-muted" />
                ) : null}
                {nr ? (
                  <Link
                    to={carryFrom(
                      `${runHref.split('?')[0] ?? ''}?step=${encodeURIComponent(n.id)}`,
                      location.search,
                    )}
                    aria-label={t.openStepLabel(n.name)}
                    className={linkClass}
                  >
                    {t.openStep}
                    <ArrowRight aria-hidden="true" className="size-icon-sm" />
                  </Link>
                ) : null}
              </li>
            );
          })}
        </ol>
      ) : null}
    </section>
  );
}
