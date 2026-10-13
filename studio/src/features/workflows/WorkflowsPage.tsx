import { ArrowDownUp, Plus, RotateCw, Search } from 'lucide-react';
import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from 'react';
import { useNavigate, useSearchParams } from 'react-router';

import { WorkflowRunDialog } from './WorkflowRunDialog';
import {
  inTab,
  normaliseQuery,
  PAGE_SIZE_KEY,
  pageWindow,
  parseListQuery,
  parsePageSize,
  searchWorkflows,
  sortWorkflows,
  tabCounts,
  writeListQuery,
  type ListQuery,
  type WorkflowSort,
  type WorkflowTab,
} from './workflow-list';
import { NewWorkflowDialog } from './NewWorkflowDialog';
import { WorkflowsTable } from './WorkflowsTable';
import { ApiRequestError, listDefinitions } from '@/api/client';
import type { DefinitionListItem } from '@/api/types';
import { PAGE_SIZES, type PageSize } from '@/components/list/page-size';
import { Pager } from '@/components/list/Pager';
import { Select } from '@/components/list/Select';
import { PageHeader } from '@/components/shell/PageHeader';
import { useShellShortcutGroups } from '@/components/shell/shell-shortcuts';
import { Skeleton, StateBlock } from '@/components/state/StateBlock';
import { Button } from '@/components/ui/button';
import { controlClasses } from '@/components/ui/field';
import { Tabs } from '@/components/ui/tabs';
import { useCopy } from '@/lib/copy';
import { useListMemory } from '@/lib/nav/use-list-memory';
import { useKeyShortcut } from '@/lib/shortcuts';
import { readString, writeString } from '@/lib/storage';
import { useDocumentTitle } from '@/lib/use-document-title';
import { cn } from '@/lib/utils';

type LoadState =
  | { kind: 'loading' }
  | { kind: 'loaded'; items: DefinitionListItem[] }
  | { kind: 'error'; status: number; code: string };

/**
 * The Workflows list: every saved workflow with its last run, filtered by tab and search,
 * sorted and paged in the browser. The list state lives in the URL so a return to the
 * page restores it.
 */
export function WorkflowsPage() {
  const copy = useCopy();
  const w = copy.workflows;
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const [query, setQuery] = useState<ListQuery>(() => parseListQuery(params));
  const [size, setSize] = useState<PageSize>(() =>
    parsePageSize(readString('local', PAGE_SIZE_KEY)),
  );
  const [state, setState] = useState<LoadState>({ kind: 'loading' });
  const [attempt, setAttempt] = useState(0);
  const [newOpen, setNewOpen] = useState(false);
  const searchRef = useRef<HTMLInputElement>(null);
  const tabsRef = useRef<HTMLDivElement>(null);
  const [runFor, setRunFor] = useState<DefinitionListItem | null>(null);

  useDocumentTitle(copy.pageTitles.workflows);
  useListMemory('workflows', state.kind !== 'loading');
  useShellShortcutGroups([
    {
      title: w.shortcuts.group,
      rows: [
        { keys: ['/'], description: w.shortcuts.search },
        {
          keys: [copy.shortcuts.keys.up, copy.shortcuts.keys.down],
          description: copy.shortcuts.moveRows,
        },
        { keys: [copy.shortcuts.keys.enter], description: w.shortcuts.open },
        { keys: ['Shift', 'F10'], description: w.shortcuts.rowMenu },
      ],
    },
  ]);
  useKeyShortcut('/', () => searchRef.current?.focus(), { enabled: state.kind === 'loaded' });

  useEffect(() => {
    const controller = new AbortController();
    listDefinitions(controller.signal)
      .then((items) => {
        setState({ kind: 'loaded', items });
      })
      .catch((error: unknown) => {
        if (controller.signal.aborted) return;
        setState(
          error instanceof ApiRequestError
            ? { kind: 'error', status: error.status, code: error.code }
            : { kind: 'error', status: 503, code: 'DEPENDENCY_UNAVAILABLE' },
        );
      });
    return () => {
      controller.abort();
    };
  }, [attempt]);

  const items = state.kind === 'loaded' ? state.items : null;
  const q = normaliseQuery(query.q);
  const matched = useMemo(() => (items ? searchWorkflows(items, query.q) : []), [items, query.q]);
  const counts = useMemo(() => tabCounts(matched), [matched]);
  const inThisTab = useMemo(
    () =>
      sortWorkflows(
        matched.filter((m) => inTab(m.item, query.tab)),
        query.sort,
      ),
    [matched, query.tab, query.sort],
  );
  const win = pageWindow(inThisTab.length, query.page, size);
  const rows = inThisTab.slice(win.start, win.end);

  // The URL follows the list state (defaults left out), with the page clamped once the
  // list is known, so a shared or restored address shows the same rows.
  const shownPage = items ? win.page : query.page;
  useEffect(() => {
    const next = writeListQuery(params, { ...query, page: shownPage });
    if (next.toString() !== params.toString()) setParams(next, { replace: true });
  }, [query, shownPage, params, setParams]);

  const update = (patch: Partial<ListQuery>) => {
    setQuery((prev) => ({ ...prev, page: 1, ...patch }));
  };

  const clearSearch = () => {
    update({ q: '' });
    searchRef.current?.focus();
  };

  const onSearchKey = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'Escape' && query.q) {
      e.preventDefault();
      e.stopPropagation();
      update({ q: '' });
    }
  };

  const empty = items !== null && items.length === 0;
  const busy = state.kind === 'loading';

  const body = (() => {
    if (state.kind === 'loading') return <Skeleton kind="rows" count={6} />;
    if (state.kind === 'error') {
      return (
        <StateBlock
          kind="error"
          title={w.states.loadFailed}
          code={copy.states.errorCode(state.status, state.code)}
          action={{
            label: copy.retry,
            icon: <RotateCw aria-hidden="true" className="size-icon-md" />,
            onClick: () => {
              setState({ kind: 'loading' });
              setAttempt((n) => n + 1);
            },
          }}
        />
      );
    }
    if (empty) {
      return (
        <StateBlock
          kind="empty"
          title={w.states.emptyTitle}
          body={w.states.emptyBody}
          action={{
            label: w.newWorkflow,
            icon: <Plus aria-hidden="true" className="size-icon-md" />,
            primary: true,
            onClick: () => {
              setNewOpen(true);
            },
          }}
        />
      );
    }
    if (rows.length === 0) {
      const searched = query.q.trim();
      return searched ? (
        <StateBlock
          kind="nomatch"
          title={w.states.noMatch(searched, query.tab !== 'all')}
          action={{ label: w.states.clearSearch, onClick: clearSearch }}
        />
      ) : (
        <StateBlock
          kind="nomatch"
          title={query.tab === 'never' ? w.states.noNever : w.states.noFailed}
          action={{
            label: w.states.showAll,
            onClick: () => {
              update({ tab: 'all' });
              requestAnimationFrame(() => {
                tabsRef.current?.querySelector<HTMLElement>('[role="tab"]')?.focus();
              });
            },
          }}
        />
      );
    }
    return <WorkflowsTable rows={rows} q={q} onRun={setRunFor} />;
  })();

  const toolbar = (
    <div className="flex flex-wrap items-center gap-2 border-b border-border-subtle px-4 py-3">
      <div className="relative flex items-center" style={{ width: 260 }}>
        <Search
          aria-hidden="true"
          className="pointer-events-none absolute left-3 size-icon-sm text-fg-muted"
        />
        <input
          ref={searchRef}
          type="search"
          value={query.q}
          onChange={(e) => {
            update({ q: e.target.value });
          }}
          onKeyDown={onSearchKey}
          placeholder={w.search.placeholder}
          aria-label={w.search.label}
          aria-keyshortcuts="/"
          autoComplete="off"
          spellCheck={false}
          className={cn(controlClasses, 'h-control-md pl-8 text-sm')}
        />
      </div>
      <span className="flex-1" />
      <Select<WorkflowSort>
        label={w.sort.label}
        value={query.sort}
        prefix={<ArrowDownUp aria-hidden="true" />}
        options={[
          { value: 'updated', label: w.sort.updated },
          { value: 'name', label: w.sort.name },
        ]}
        onChange={(v) => {
          update({ sort: v });
        }}
        className="text-sm"
      />
    </div>
  );

  const showTools = state.kind === 'loading' || (state.kind === 'loaded' && !empty);

  return (
    <>
      <PageHeader
        title={w.title}
        actions={
          empty ? null : (
            <Button
              onClick={() => {
                setNewOpen(true);
              }}
            >
              <Plus aria-hidden="true" />
              {w.newWorkflow}
            </Button>
          )
        }
      />

      <main style={{ maxWidth: 1280 }} className="mx-auto w-full flex-1 px-8 pt-8 pb-12">
        <div
          aria-busy={busy || undefined}
          className="overflow-hidden rounded-lg border border-border bg-surface"
        >
          {showTools ? (
            <div ref={tabsRef}>
              <Tabs<WorkflowTab>
                label={w.tabsLabel}
                value={query.tab}
                size="md"
                countBadges
                listClassName="gap-6 px-4 pt-2"
                items={(['all', 'failed', 'never'] as const).map((t) => ({
                  value: t,
                  label: w.tabs[t],
                  count: busy ? undefined : counts[t],
                }))}
                onValueChange={(tab) => {
                  update({ tab });
                }}
              >
                {toolbar}
                {body}
                {inThisTab.length > PAGE_SIZES[0] ? (
                  <Pager
                    from={win.start + 1}
                    to={win.end}
                    total={inThisTab.length}
                    noun={w.noun}
                    size={size}
                    onSize={(n) => {
                      setSize(n);
                      writeString('local', PAGE_SIZE_KEY, String(n));
                      update({});
                    }}
                    hasPrevious={win.page > 1}
                    hasNext={win.page < win.pages}
                    onPrevious={() => {
                      setQuery((prev) => ({ ...prev, page: win.page - 1 }));
                    }}
                    onNext={() => {
                      setQuery((prev) => ({ ...prev, page: win.page + 1 }));
                    }}
                  />
                ) : null}
              </Tabs>
            </div>
          ) : (
            body
          )}
        </div>
      </main>

      <NewWorkflowDialog
        open={newOpen}
        onClose={() => {
          setNewOpen(false);
        }}
        onCreate={(name, description) => {
          setNewOpen(false);
          // Nothing is saved yet: the Editor holds a local draft until its first Save
          // creates version 1, so the name and description travel as router state.
          void navigate('/workflows/new', { state: { name, description } });
        }}
      />
      <WorkflowRunDialog
        item={runFor}
        onClose={() => {
          setRunFor(null);
        }}
      />
    </>
  );
}
