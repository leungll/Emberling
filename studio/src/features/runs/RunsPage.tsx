import { Clock, Search } from 'lucide-react';
import { useEffect, useMemo, useRef, useState } from 'react';
import { useLocation, useNavigate } from 'react-router';

import { RunWorkflowButton } from './RunWorkflowButton';
import { RunsTable } from './RunsTable';
import {
  hasFilters,
  nextPage,
  offsetOf,
  parseRunsQuery,
  previousPage,
  runHref,
  runsSearch,
  startedAfter,
  storePageSize,
  storedPageSize,
  toListQuery,
  withFilters,
  type RunsQuery,
} from './runs-url';
import { StartRunDialog } from './StartRunDialog';
import { ApiRequestError, listDefinitions, listRuns } from '@/api/client';
import type { DefinitionListItem, RunListResponse, RunStatusCounts } from '@/api/types';
import { Pager } from '@/components/list/Pager';
import { PAGE_SIZES } from '@/components/list/page-size';
import { Select } from '@/components/list/Select';
import { PageHeader } from '@/components/shell/PageHeader';
import { useShellShortcutGroups } from '@/components/shell/shell-shortcuts';
import { Skeleton, StateBlock } from '@/components/state/StateBlock';
import { Tabs } from '@/components/ui/tabs';
import { useCopy } from '@/lib/copy';
import { useListMemory } from '@/lib/nav/use-list-memory';
import { STATUS_PARAMS, type RangeParam, type StatusParam } from '@/lib/run-words';
import { useKeyShortcut } from '@/lib/shortcuts';
import { showToast } from '@/lib/toast';
import { useDocumentTitle } from '@/lib/use-document-title';
import { cn } from '@/lib/utils';

type TabValue = 'all' | StatusParam;

const COUNT_OF: Record<TabValue, keyof RunStatusCounts> = {
  all: 'all',
  running: 'running',
  waiting: 'paused',
  failed: 'failed',
  completed: 'completed',
};

interface Loaded {
  key: string;
  page: RunListResponse;
}

/** "abc12345", "run_abc12345" or a full run ID: what the server's code filter accepts. */
const CODE = /^(run_)?[0-9a-z]{8,64}$/i;

/**
 * Runs: every run of every workflow, newest first, from the server's run list. Status tabs
 * carry the server's counts; the workflow and start-time filters, the page and its size live
 * in the address. Nothing here counts or orders runs itself.
 */
export function RunsPage() {
  const copy = useCopy();
  const t = copy.runs.page;
  const location = useLocation();
  const navigate = useNavigate();
  const query = useMemo(() => parseRunsQuery(location.search, storedPageSize()), [location.search]);
  const key = runsSearch(query);

  const [loaded, setLoaded] = useState<Loaded | null>(null);
  const [failure, setFailure] = useState<{ key: string; attempt: number; error: unknown } | null>(
    null,
  );
  const [attempt, setAttempt] = useState(0);
  const [workflows, setWorkflows] = useState<DefinitionListItem[] | null>(null);
  const [runWorkflow, setRunWorkflow] = useState<string | null>(null);
  const gotoRef = useRef<HTMLInputElement>(null);
  const [goto, setGoto] = useState('');

  useDocumentTitle(copy.pageTitles.runs);
  useShellShortcutGroups([
    {
      title: t.shortcuts.group,
      rows: [
        { keys: ['/'], description: t.shortcuts.goTo },
        { keys: ['↑', '↓'], description: t.shortcuts.move },
        { keys: [copy.shortcuts.keys.enter], description: t.shortcuts.open },
      ],
    },
  ]);
  useKeyShortcut('/', () => {
    gotoRef.current?.focus();
    gotoRef.current?.select();
  });

  const current = loaded?.key === key ? loaded.page : null;
  useListMemory('runs', current !== null);

  useEffect(() => {
    // `key` is the whole query in canonical form, so the effect reads the query from it.
    const query = parseRunsQuery(key);
    if (query.cursor && offsetOf(query.cursor) === null) {
      void navigate({ search: runsSearch({ ...query, cursor: null }) }, { replace: true });
      return;
    }
    const controller = new AbortController();
    const after = startedAfter(query.range, query.cursor !== null, new Date());
    listRuns(toListQuery(query, after), controller.signal)
      .then((page) => {
        setLoaded({ key, page });
      })
      .catch((e: unknown) => {
        if (controller.signal.aborted) return;
        // A cursor issued for other filters (or another start instant) is refused: start
        // again from the first page rather than showing an error.
        if (e instanceof ApiRequestError && e.status === 400 && query.cursor) {
          void navigate({ search: runsSearch({ ...query, cursor: null }) }, { replace: true });
          return;
        }
        setFailure({ key, attempt, error: e });
      });
    return () => {
      controller.abort();
    };
  }, [key, attempt, navigate]);

  useEffect(() => {
    const controller = new AbortController();
    listDefinitions(controller.signal).then(setWorkflows, () => {
      // The workflow filter and the Run workflow list stay empty; the runs still load.
    });
    return () => {
      controller.abort();
    };
  }, []);

  const go = (next: RunsQuery) => {
    void navigate(
      { search: runsSearch(next, new URLSearchParams(location.search)) },
      { replace: true },
    );
  };

  // A failure belongs to the request that made it; a retry or another query clears it.
  const error = failure?.key === key && failure.attempt === attempt ? failure.error : null;

  const counts = (current ?? loaded?.page)?.counts ?? null;
  const tab: TabValue = query.status ?? 'all';
  const filtered = hasFilters(query);
  const everEmpty = counts !== null && counts.all === 0 && !filtered;

  const onGoto = () => {
    const raw = goto.trim();
    if (!raw) return;
    if (!CODE.test(raw)) {
      showToast(t.goToShort);
      return;
    }
    listRuns({ code: raw, limit: 2 })
      .then((page) => {
        const [first, second] = page.items;
        if (first && !second) {
          void navigate(runHref(first));
          return;
        }
        showToast(second ? t.goToMany(raw) : t.goToNone(raw));
        gotoRef.current?.select();
      })
      .catch(() => {
        showToast(t.goToFailed);
      });
  };

  const wfOptions = useMemo(
    () => [
      { value: '', label: t.allWorkflows },
      ...(workflows ?? [])
        .map((w) => ({ value: w.workflowId, label: w.name, search: w.workflowId }))
        .sort((a, b) => a.label.localeCompare(b.label)),
    ],
    [workflows, t.allWorkflows],
  );
  const rangeOptions: { value: RangeParam | 'all'; label: string }[] = [
    { value: 'all', label: t.ranges.all },
    { value: '24h', label: t.ranges['24h'] },
    { value: '7d', label: t.ranges['7d'] },
  ];

  const header = (
    <PageHeader
      title={t.title}
      actions={<RunWorkflowButton workflows={workflows} onPick={setRunWorkflow} />}
    />
  );
  const dialog = (
    <StartRunDialog
      workflowId={runWorkflow}
      fromRuns
      onClose={() => {
        setRunWorkflow(null);
      }}
    />
  );

  const errorBlock =
    error !== null ? (
      <StateBlock
        kind="error"
        title={copy.states.loadFailed(copy.states.what.runs)}
        code={
          error instanceof ApiRequestError
            ? copy.states.errorCode(error.status, error.code)
            : undefined
        }
        action={{
          label: copy.retry,
          primary: true,
          onClick: () => {
            setAttempt((a) => a + 1);
          },
        }}
      />
    ) : null;

  let body;
  if (errorBlock && !current) {
    body = errorBlock;
  } else if (!current) {
    body = (
      <div aria-busy="true">
        <Skeleton kind="rows" count={6} />
      </div>
    );
  } else if (current.items.length === 0) {
    body = filtered ? (
      <StateBlock
        kind="nomatch"
        title={t.noMatch}
        action={{
          label: t.clearFilters,
          onClick: () => {
            go(withFilters(query, { wf: '', range: null }));
          },
        }}
      />
    ) : (
      <StateBlock
        kind="nomatch"
        title={t.emptyTab[tab]}
        action={{
          label: t.showAll,
          onClick: () => {
            go(withFilters(query, { status: null }));
          },
        }}
      />
    );
  } else {
    // A cursor this tab never followed (a pasted address) has no known position; the page
    // then starts again from the first page (see the effect below).
    const offset = offsetOf(query.cursor) ?? 0;
    const total = counts ? counts[COUNT_OF[tab]] : null;
    const from = offset + 1;
    body = (
      <>
        <div className="overflow-x-auto">
          <RunsTable items={current.items} busy={loaded?.key !== key} />
        </div>
        {total !== null && total > PAGE_SIZES[0] ? (
          <Pager
            from={from}
            to={from + current.items.length - 1}
            total={total}
            noun={t.noun}
            size={query.size}
            onSize={(size) => {
              storePageSize(size);
              go(withFilters(query, { size }));
            }}
            hasPrevious={query.cursor !== null}
            hasNext={current.nextCursor !== null}
            onPrevious={() => {
              go(previousPage(query, current.prevCursor, offset));
            }}
            onNext={() => {
              if (current.nextCursor) {
                go(nextPage(query, current.nextCursor, offset, current.items.length));
              }
            }}
          />
        ) : null}
      </>
    );
  }

  if (everEmpty && !query.status) {
    return (
      <>
        {header}
        <main
          style={{ maxWidth: 1280 }}
          className="mx-auto w-full flex-1 p-6 pb-12 xl:p-8 xl:pb-12"
        >
          <div className="rounded-lg border border-border bg-surface">
            <StateBlock
              kind="empty"
              title={t.empty.title}
              body={t.empty.body}
              action={{ label: t.empty.action, to: '/workflows' }}
            />
          </div>
        </main>
        {dialog}
      </>
    );
  }

  return (
    <>
      {header}
      <main style={{ maxWidth: 1280 }} className="mx-auto w-full flex-1 p-6 pb-12 xl:p-8 xl:pb-12">
        <div className="rounded-lg border border-border bg-surface">
          {errorBlock && !current ? (
            errorBlock
          ) : (
            <Tabs
              label={t.statusTabs}
              value={tab}
              countBadges
              size="md"
              listClassName="gap-6 px-4 pt-2"
              items={(['all', ...STATUS_PARAMS] as TabValue[]).map((v) => ({
                value: v,
                label: t.tabs[v],
                ...(counts ? { count: counts[COUNT_OF[v]] } : {}),
              }))}
              onValueChange={(v) => {
                go(withFilters(query, { status: v === 'all' ? null : v }));
              }}
            >
              <div className="flex flex-wrap items-center gap-3 border-b border-border-subtle px-4 py-3">
                <Select
                  label={t.workflow}
                  value={query.wf}
                  options={wfOptions}
                  search={t.searchWorkflows}
                  noMatch={t.noWorkflowMatch}
                  onChange={(wf) => {
                    go(withFilters(query, { wf }));
                  }}
                />
                <Select
                  label={t.started}
                  value={query.range ?? 'all'}
                  options={rangeOptions}
                  prefix={<Clock aria-hidden="true" />}
                  onChange={(v) => {
                    go(withFilters(query, { range: v === 'all' ? null : v }));
                  }}
                />
                {filtered ? (
                  <button
                    type="button"
                    onClick={() => {
                      go(withFilters(query, { wf: '', range: null }));
                    }}
                    className="inline-flex h-control-md items-center rounded-sm px-1 text-md font-medium text-accent-fg hover:underline"
                  >
                    {t.clearFilters}
                  </button>
                ) : null}
                <span className="flex-1" />
                <form
                  role="search"
                  onSubmit={(e) => {
                    e.preventDefault();
                    onGoto();
                  }}
                  className="relative flex items-center"
                >
                  <Search
                    aria-hidden="true"
                    className="pointer-events-none absolute left-2 size-icon-sm text-fg-muted"
                  />
                  <input
                    ref={gotoRef}
                    value={goto}
                    onChange={(e) => {
                      setGoto(e.target.value);
                    }}
                    onKeyDown={(e) => {
                      if (e.key === 'Escape' && goto) {
                        e.stopPropagation();
                        setGoto('');
                      }
                    }}
                    placeholder={t.goTo}
                    aria-label={t.goTo}
                    aria-keyshortcuts="/"
                    autoComplete="off"
                    spellCheck={false}
                    style={{ width: 200 }}
                    className={cn(
                      'h-control-md rounded-md border border-border-control bg-surface-inset pr-2 pl-8 font-mono text-sm text-fg placeholder:font-sans placeholder:text-fg-placeholder',
                    )}
                  />
                </form>
              </div>
              {body}
            </Tabs>
          )}
        </div>
      </main>
      {dialog}
    </>
  );
}
