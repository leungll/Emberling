import { useEffect, useMemo, useState, type ReactNode } from 'react';
import { Outlet, useLocation } from 'react-router';

import { AppSidebar, type NavCounts } from './AppSidebar';
import { listDefinitions, listRuns } from '@/api/client';
import type { DefinitionListItem, RunStatusCounts } from '@/api/types';
import { LIST_PATH, listHref, type ListName } from '@/lib/nav/list-state';
import { readRecent, recentWorkflows } from '@/lib/nav/recent';
import { useSidebarCollapsed } from '@/lib/nav/use-sidebar';
import { useKeyShortcut } from '@/lib/shortcuts';
import { useReadiness } from '@/lib/use-readiness';
import { useShortcutsStore } from '@/stores/shortcuts-store';

function sectionOf(pathname: string): ListName | null {
  if (pathname === '/runs' || pathname.startsWith('/runs/')) return 'runs';
  if (pathname === '/workflows' || pathname.startsWith('/workflows/')) return 'workflows';
  return null;
}

/**
 * The frame of the list pages and of not-found pages: the sidebar and a page column that
 * scrolls with the window. The sidebar's workflow count and Recent come from the workflow
 * list, the Runs count from the run list's counts; each shows nothing extra while its list is
 * loading or failed.
 */
export function ListShell({ children }: { children?: ReactNode }) {
  const { pathname } = useLocation();
  const [items, setItems] = useState<DefinitionListItem[] | null>(null);
  const [runCounts, setRunCounts] = useState<RunStatusCounts | null>(null);
  const connection = useReadiness();
  const [collapsed, toggle] = useSidebarCollapsed();
  const openSheet = useShortcutsStore((s) => s.setOpen);
  useKeyShortcut('[', toggle);

  useEffect(() => {
    const controller = new AbortController();
    listDefinitions(controller.signal).then(setItems, () => {
      // The sidebar is a convenience; the page reports its own load errors.
    });
    return () => {
      controller.abort();
    };
  }, []);

  // The Runs count and its live dot are the server's counts over every run; read again on
  // each page change so a run started elsewhere shows up.
  useEffect(() => {
    const controller = new AbortController();
    listRuns({ limit: 1 }, controller.signal).then(
      (page) => {
        setRunCounts(page.counts);
      },
      () => {
        // A convenience like the workflow count: the Runs page reports its own errors.
      },
    );
    return () => {
      controller.abort();
    };
  }, [pathname]);

  const section = sectionOf(pathname);
  const onListPage = section !== null && pathname === LIST_PATH[section];
  const hrefs: Record<ListName, string> = {
    runs: onListPage && section === 'runs' ? LIST_PATH.runs : listHref('runs', true),
    workflows:
      onListPage && section === 'workflows' ? LIST_PATH.workflows : listHref('workflows', true),
  };
  const counts: NavCounts = {
    runs: runCounts
      ? { total: runCounts.all, running: runCounts.running, waiting: runCounts.paused }
      : null,
    workflows: items ? items.length : null,
  };
  const recent = useMemo(() => (items ? recentWorkflows(items, readRecent()) : []), [items]);

  return (
    <div className="flex min-h-screen bg-bg text-fg">
      <AppSidebar
        section={section}
        onListPage={onListPage}
        hrefs={hrefs}
        counts={counts}
        recent={recent}
        connection={connection}
        collapsed={collapsed}
        onToggle={toggle}
        onShortcuts={() => {
          openSheet(true);
        }}
      />
      <div className="flex min-w-0 flex-1 flex-col">{children ?? <Outlet />}</div>
    </div>
  );
}
