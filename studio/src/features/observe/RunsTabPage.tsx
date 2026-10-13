import { Play, RotateCw } from 'lucide-react';
import { useEffect, useState } from 'react';
import { useLocation, useNavigate, useParams } from 'react-router';

import { RunAgainDialog } from './RunAgainDialog';
import { RunList, type RunListFilters } from './RunList';
import { RunView } from './RunView';
import { parseRunView, runViewSearch, type RunViewState } from './run-view-url';
import { useRegistry, useRunData } from './useRunData';
import { useWorkflowRuns } from './useWorkflowRuns';
import { ApiRequestError, getDefinition } from '@/api/client';
import type { Definition } from '@/api/types';
import { IdChip } from '@/components/id/IdChip';
import { ListShell } from '@/components/shell/ListShell';
import { NotFoundView } from '@/components/shell/NotFoundView';
import { WorkflowHeader } from '@/components/shell/WorkflowHeader';
import { StateBlock } from '@/components/state/StateBlock';
import { Button } from '@/components/ui/button';
import { StartRunDialog } from '@/features/runs/StartRunDialog';
import { useCopy } from '@/lib/copy';
import { fromRuns } from '@/lib/nav/list-state';
import { noteRecent } from '@/lib/nav/recent';
import { shortCode } from '@/lib/short-code';
import { useDocumentTitle } from '@/lib/use-document-title';

/**
 * A workflow's Runs tab: the workflow header, the workflow's runs on the left and the
 * open run on the right. Without a run in the address it opens the newest one.
 */

type WorkflowState =
  | { id: string; kind: 'loading' }
  | { id: string; kind: 'loaded'; definition: Definition }
  | { id: string; kind: 'missing' }
  | { id: string; kind: 'error' };

const RESET: Partial<RunViewState> = {
  el: null,
  tab: null,
  tq: '',
  fails: false,
  group: 'all',
  q: '',
  only: false,
};

export function RunsTabPage() {
  const { workflowId = '', runId = '' } = useParams();
  const { search } = useLocation();
  const navigate = useNavigate();
  const copy = useCopy();
  const ui = parseRunView(search);
  const registry = useRegistry();

  const [workflow, setWorkflow] = useState<WorkflowState>({ id: workflowId, kind: 'loading' });
  const [workflowAttempt, setWorkflowAttempt] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    getDefinition(workflowId, controller.signal).then(
      (definition) => {
        noteRecent(workflowId);
        setWorkflow({ id: workflowId, kind: 'loaded', definition });
      },
      (error: unknown) => {
        if (controller.signal.aborted) return;
        const missing = error instanceof ApiRequestError && error.status === 404;
        setWorkflow({ id: workflowId, kind: missing ? 'missing' : 'error' });
      },
    );
    return () => {
      controller.abort();
    };
  }, [workflowId, workflowAttempt]);
  const wf: WorkflowState =
    workflow.id === workflowId ? workflow : { id: workflowId, kind: 'loading' };
  const latest = wf.kind === 'loaded' ? wf.definition : null;

  const [runAttempt, setRunAttempt] = useState(0);
  const data = useRunData(runId, runAttempt, registry.nodeTypes);
  const run = runId && data.observed.snapshot?.run.id === runId ? data.observed.snapshot.run : null;

  // The run list is read again when the open run changes status or is not in it yet.
  const [reload, setReload] = useState(0);
  const list = useWorkflowRuns(workflowId, ui.status, ui.range, reload);
  const listed = list.items.find((i) => i.id === runId);
  const stale =
    list.state === 'ready' &&
    !ui.status &&
    !ui.range &&
    run !== null &&
    listed?.status !== run.status;
  const staleKey = stale && run ? `${run.id}|${run.status}` : '';
  const [refreshedFor, setRefreshedFor] = useState('');
  if (staleKey && staleKey !== refreshedFor) {
    setRefreshedFor(staleKey);
    setReload((n) => n + 1);
  }

  // A run always opens under its own workflow.
  const runWorkflow = run?.workflowId;
  useEffect(() => {
    if (!runWorkflow || runWorkflow === workflowId) return;
    void navigate(
      `/workflows/${encodeURIComponent(runWorkflow)}/runs/${encodeURIComponent(runId)}${search}`,
      { replace: true },
    );
  }, [runWorkflow, workflowId, runId, search, navigate]);

  // Without a run in the address, open the newest one under the filters.
  const newest = list.items[0]?.id;
  useEffect(() => {
    if (runId || !newest) return;
    void navigate(
      {
        pathname: `/workflows/${encodeURIComponent(workflowId)}/runs/${encodeURIComponent(newest)}`,
        search,
      },
      { replace: true },
    );
  }, [runId, newest, workflowId, search, navigate]);

  const [starting, setStarting] = useState(false);
  const [again, setAgain] = useState(false);

  const name = latest?.name ?? '';
  useDocumentTitle(
    name
      ? runId
        ? copy.pageTitles.run(name, shortCode(runId))
        : copy.pageTitles.workflowRuns(name)
      : copy.nav.runs,
  );

  if (wf.kind === 'missing') {
    return (
      <ListShell>
        <NotFoundView kind="workflow" id={workflowId} />
      </ListShell>
    );
  }

  const openRun = (id: string, how: 'click' | 'key') => {
    void navigate(
      {
        pathname: `/workflows/${encodeURIComponent(workflowId)}/runs/${encodeURIComponent(id)}`,
        search: runViewSearch({ ...ui, ...RESET }, search),
      },
      { replace: how === 'key' },
    );
  };

  const setFilters = (f: RunListFilters) => {
    void navigate(
      { search: runViewSearch({ ...ui, status: f.status, range: f.range }, search) },
      { replace: true },
    );
  };

  const v = copy.runs.view;
  let body;
  if (wf.kind === 'error') {
    body = (
      <StateBlock
        kind="error"
        title={copy.states.loadFailed(copy.states.what.workflow)}
        body={copy.states.errorBody}
        action={{
          label: copy.retry,
          icon: <RotateCw aria-hidden="true" className="size-icon-sm" />,
          onClick: () => {
            setWorkflowAttempt((n) => n + 1);
          },
        }}
        className="flex-1 justify-center"
      />
    );
  } else if (runId) {
    body = (
      <RunView
        key={runId}
        runId={runId}
        workflowId={workflowId}
        latestVersion={latest?.version ?? null}
        registry={registry}
        data={data}
        onRetry={() => {
          setRunAttempt((n) => n + 1);
        }}
        onRunAgain={() => {
          if (run) setAgain(true);
        }}
      />
    );
  } else if (list.state === 'ready' && !list.items.length) {
    const filtered = ui.status !== null || ui.range !== null;
    body = (
      <main className="flex min-w-0 flex-1 flex-col bg-bg">
        <StateBlock
          kind={filtered ? 'nomatch' : 'empty'}
          title={filtered ? copy.runs.list.noMatch : v.noRuns}
          body={filtered ? undefined : v.noRunsBody}
          action={
            filtered
              ? undefined
              : {
                  label: v.run,
                  primary: true,
                  icon: <Play aria-hidden="true" className="size-icon-sm" />,
                  onClick: () => {
                    setStarting(true);
                  },
                }
          }
          className="flex-1 justify-center"
        />
      </main>
    );
  } else if (list.state === 'error') {
    body = <main className="flex min-w-0 flex-1 flex-col bg-bg" />;
  } else {
    body = (
      <main
        aria-busy="true"
        aria-label={copy.loading}
        className="flex min-w-0 flex-1 flex-col bg-bg"
      />
    );
  }

  return (
    <div className="flex h-screen min-h-0 flex-col bg-bg text-fg">
      <WorkflowHeader
        workflowId={workflowId}
        name={name}
        tab="runs"
        runId={runId ? runId : undefined}
        identity={
          // Below the wide layout the header keeps one row for the name and the tabs.
          <span className="hidden xl:inline-flex">
            <IdChip
              id={workflowId}
              kind={v.workflowId}
              label={v.workflowId.toLowerCase()}
              size="md"
            />
          </span>
        }
        actions={
          <>
            {run ? (
              <Button
                variant="secondary"
                tooltip={v.runAgainTip}
                onClick={() => {
                  setAgain(true);
                }}
              >
                <RotateCw aria-hidden="true" className="size-icon-sm" />
                {v.runAgain}
              </Button>
            ) : null}
            <Button
              variant="primary"
              onClick={() => {
                setStarting(true);
              }}
            >
              <Play aria-hidden="true" className="size-icon-sm" />
              {v.run}
            </Button>
          </>
        }
      />
      <div className="flex min-h-0 flex-1">
        {wf.kind === 'error' ? null : (
          <RunList
            items={list.items}
            counts={list.counts}
            state={list.state}
            hasMore={list.hasMore}
            loadingMore={list.loadingMore}
            onMore={list.loadMore}
            onRetry={() => {
              setReload((n) => n + 1);
            }}
            selectedId={runId ? runId : null}
            filters={{ status: ui.status, range: ui.range }}
            onFilters={setFilters}
            onOpen={openRun}
          />
        )}
        {body}
      </div>
      <StartRunDialog
        workflowId={starting ? workflowId : null}
        onClose={() => {
          setStarting(false);
        }}
        fromRuns={fromRuns(search)}
      />
      <RunAgainDialog
        open={again}
        onClose={() => {
          setAgain(false);
        }}
        run={run}
        definition={data.definition}
        definitionFailed={data.definitionFailed}
        latestVersion={latest?.version ?? null}
        search={search}
      />
    </div>
  );
}
