import { useEffect, useState } from 'react';
import { useLocation, useParams } from 'react-router';

import { listDefinitions } from '@/api/client';
import type { DefinitionListItem } from '@/api/types';
import { useShellShortcutGroups } from '@/components/shell/shell-shortcuts';
import { ListShell } from '@/components/shell/ListShell';
import { NotFoundView } from '@/components/shell/NotFoundView';
import { WorkflowHeader } from '@/components/shell/WorkflowHeader';
import { Skeleton, StateBlock } from '@/components/state/StateBlock';
import { useCopy } from '@/lib/copy';
import { carryFrom } from '@/lib/nav/list-state';
import { noteRecent } from '@/lib/nav/recent';
import { useDocumentTitle } from '@/lib/use-document-title';

type State =
  | { kind: 'loading' }
  | { kind: 'loaded'; item: DefinitionListItem }
  | { kind: 'missing' }
  | { kind: 'error' };

/**
 * A workflow's Runs tab without a run chosen. Until the Backend can list a workflow's runs,
 * it offers the workflow's last run, which opens in this tab.
 */
export function WorkflowRunsPlaceholder() {
  const { workflowId = '' } = useParams();
  const { search } = useLocation();
  const copy = useCopy();
  const p = copy.placeholders.workflowRuns;
  const [state, setState] = useState<State>({ kind: 'loading' });
  useShellShortcutGroups();
  useDocumentTitle(
    state.kind === 'loaded' ? copy.pageTitles.workflowRuns(state.item.name) : copy.nav.runs,
  );

  useEffect(() => {
    const controller = new AbortController();
    listDefinitions(controller.signal).then(
      (items) => {
        const item = items.find((d) => d.workflowId === workflowId);
        if (!item) {
          setState({ kind: 'missing' });
          return;
        }
        noteRecent(workflowId);
        setState({ kind: 'loaded', item });
      },
      () => {
        if (!controller.signal.aborted) setState({ kind: 'error' });
      },
    );
    return () => {
      controller.abort();
    };
  }, [workflowId]);

  if (state.kind === 'missing') {
    return (
      <ListShell>
        <NotFoundView kind="workflow" id={workflowId} />
      </ListShell>
    );
  }

  const base = `/workflows/${encodeURIComponent(workflowId)}`;
  const item = state.kind === 'loaded' ? state.item : null;
  return (
    <div className="flex min-h-screen flex-col bg-bg text-fg">
      <WorkflowHeader workflowId={workflowId} name={item?.name ?? ''} tab="runs" />
      <main className="flex flex-1 flex-col">
        {state.kind === 'loading' ? <Skeleton kind="text" className="p-8" /> : null}
        {state.kind === 'error' ? (
          <StateBlock
            kind="error"
            title={copy.states.loadFailed(copy.states.what.workflow)}
            body={copy.states.errorBody}
            className="flex-1 justify-center"
          />
        ) : null}
        {item ? (
          item.lastRun ? (
            <StateBlock
              kind="empty"
              title={p.title}
              action={{
                label: p.action,
                to: carryFrom(`${base}/runs/${encodeURIComponent(item.lastRun.id)}`, search),
              }}
              className="flex-1 justify-center"
            />
          ) : (
            <StateBlock
              kind="empty"
              title={p.none}
              body={p.noneBody}
              action={{ label: p.toEditor, to: carryFrom(base, search) }}
              className="flex-1 justify-center"
            />
          )
        ) : null}
      </main>
    </div>
  );
}
