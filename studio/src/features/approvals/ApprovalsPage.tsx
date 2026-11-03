import { RotateCw } from 'lucide-react';
import { useEffect, useState } from 'react';
import { Link } from 'react-router';

import { HumanWaitsPanel } from './HumanWaitsPanel';
import { OperatorTokenField } from './OperatorTokenField';
import {
  approvalErrorText,
  classifyApprovalError,
  type ApprovalLoadError,
} from './approval-errors';
import { listApprovals } from '@/api/client';
import type { ApprovalStatus, ApprovalView } from '@/api/types';
import { IdChip } from '@/components/id/IdChip';
import { PageHeader } from '@/components/shell/PageHeader';
import { Skeleton, StateBlock } from '@/components/state/StateBlock';
import { StatusIndicator } from '@/components/status/StatusIndicator';
import { Deadline, Timestamp } from '@/components/time/Time';
import { Tabs } from '@/components/ui/tabs';
import { useCopy } from '@/lib/copy';
import { useListMemory } from '@/lib/nav/use-list-memory';
import { useDocumentTitle } from '@/lib/use-document-title';
import { cn } from '@/lib/utils';
import { useOperatorStore } from '@/stores/operator-store';

const STATUSES: readonly ApprovalStatus[] = ['PENDING', 'APPROVED', 'REJECTED', 'EXPIRED'];

type LoadState =
  | { kind: 'loading' }
  | { kind: 'loaded'; items: ApprovalView[] }
  | { kind: 'error'; error: ApprovalLoadError };

const CELL = 'px-4 py-3 align-top text-md';

/**
 * The Approvals list: Agent production calls a person must decide, one status at a time,
 * as the Backend lists them. Nothing here derives a status or a deadline; the page shows
 * stored facts and sends the viewer's decision from the detail page.
 */
export function ApprovalsPage() {
  const copy = useCopy();
  const a = copy.approvals;
  const token = useOperatorStore((s) => s.token);
  const [status, setStatus] = useState<ApprovalStatus>('PENDING');
  const [state, setState] = useState<LoadState>({ kind: 'loading' });
  const [attempt, setAttempt] = useState(0);

  useDocumentTitle(copy.pageTitles.approvals);
  useListMemory('approvals', state.kind !== 'loading');

  useEffect(() => {
    if (!token) return;
    const controller = new AbortController();
    listApprovals(status, token, controller.signal)
      .then((items) => {
        setState({ kind: 'loaded', items });
      })
      .catch((error: unknown) => {
        if (controller.signal.aborted) return;
        setState({ kind: 'error', error: classifyApprovalError(error, true) });
      });
    return () => {
      controller.abort();
    };
  }, [token, status, attempt]);

  // Every change of what is being asked for shows the list as loading at once; the effect
  // above only reports the answer.
  const reload = () => {
    setState({ kind: 'loading' });
    setAttempt((n) => n + 1);
  };
  const tokenField = <OperatorTokenField onChange={reload} />;

  let body;
  if (!token) {
    body = (
      <div className="flex flex-col gap-4 px-6 py-8">
        <p className="m-0 text-md text-fg-secondary">{a.tokenRequired}</p>
        {tokenField}
      </div>
    );
  } else if (state.kind === 'loading') {
    body = <Skeleton kind="rows" count={4} className="p-4" />;
  } else if (state.kind === 'error') {
    body = (
      <StateBlock
        kind="error"
        title={approvalErrorText(state.error)}
        code={
          state.error.kind === 'error'
            ? copy.states.errorCode(state.error.status, state.error.code)
            : undefined
        }
        action={{
          label: copy.retry,
          icon: <RotateCw aria-hidden="true" className="size-icon-md" />,
          onClick: reload,
        }}
        details={state.error.kind === 'unauthorized' ? tokenField : undefined}
      />
    );
  } else if (!state.items.length) {
    body = <StateBlock kind="empty" title={a.empty(a.status[status])} />;
  } else {
    body = <ApprovalsTable items={state.items} />;
  }

  return (
    <>
      <PageHeader title={a.title} actions={token ? tokenField : null} />
      <main style={{ maxWidth: 1280 }} className="mx-auto w-full flex-1 px-8 pt-8 pb-12">
        {token ? <HumanWaitsPanel /> : null}
        <p className="m-0 mb-4 text-md text-fg-secondary">{a.intro}</p>
        <div className="overflow-hidden rounded-lg border border-border bg-surface">
          <Tabs<ApprovalStatus>
            label={a.tabsLabel}
            value={status}
            size="md"
            listClassName="gap-6 px-4 pt-2"
            items={STATUSES.map((s) => ({ value: s, label: a.status[s] }))}
            onValueChange={(next) => {
              setState({ kind: 'loading' });
              setStatus(next);
            }}
          >
            {body}
          </Tabs>
        </div>
      </main>
    </>
  );
}

function ApprovalsTable({ items }: { items: ApprovalView[] }) {
  const copy = useCopy();
  const a = copy.approvals;
  return (
    <table className="w-full border-collapse">
      <thead>
        <tr className="border-b border-border text-left text-sm text-fg-muted">
          <th className={CELL}>{a.columns.tool}</th>
          <th className={CELL}>{a.columns.run}</th>
          <th className={CELL}>{a.columns.requested}</th>
          <th className={CELL}>{a.columns.deadline}</th>
          <th className={CELL}>{a.columns.status}</th>
        </tr>
      </thead>
      <tbody>
        {items.map((item) => (
          <tr
            key={item.id}
            className="border-b border-border-subtle last:border-b-0 hover:bg-surface-hover"
          >
            <td className={CELL}>
              <Link
                to={`/approvals/${encodeURIComponent(item.id)}`}
                className="font-mono text-md font-medium text-fg no-underline hover:underline"
              >
                {item.toolName}
              </Link>
            </td>
            <td className={CELL}>
              <IdChip id={item.runId} label={a.run.toLowerCase()} to={`/runs/${item.runId}`} />
            </td>
            <td className={cn(CELL, 'whitespace-nowrap')}>
              <Timestamp iso={item.requestedAt} />
            </td>
            <td className={cn(CELL, 'whitespace-nowrap')}>
              <span className="flex flex-col">
                <Timestamp iso={item.deadline} />
                {item.status === 'PENDING' ? (
                  <span className="text-sm text-fg-muted">
                    <Deadline iso={item.deadline} />
                  </span>
                ) : null}
              </span>
            </td>
            <td className={cn(CELL, 'whitespace-nowrap')}>
              <StatusIndicator status={{ kind: 'approval', value: item.status }} colourLabel />
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
