import { useEffect, useState } from 'react';
import { useParams } from 'react-router';

import { OperatorTokenField } from './OperatorTokenField';
import {
  approvalErrorText,
  classifyApprovalError,
  type ApprovalLoadError,
} from './approval-errors';
import { ApiRequestError, decideApproval, getApproval } from '@/api/client';
import type { Approval, ApprovalView } from '@/api/types';
import { FactList } from '@/components/facts/FactList';
import { IdChip } from '@/components/id/IdChip';
import { JsonViewer } from '@/components/json/JsonViewer';
import { PageHeader } from '@/components/shell/PageHeader';
import { Skeleton, StateBlock } from '@/components/state/StateBlock';
import { StatusIndicator } from '@/components/status/StatusIndicator';
import { Deadline, Timestamp } from '@/components/time/Time';
import { Button } from '@/components/ui/button';
import { Callout } from '@/components/ui/banner';
import { Field, TextArea } from '@/components/ui/field';
import { useCopy } from '@/lib/copy';
import { listHref } from '@/lib/nav/list-state';
import { useDocumentTitle } from '@/lib/use-document-title';
import { useOperatorStore } from '@/stores/operator-store';

type LoadState =
  | { kind: 'loading' }
  | { kind: 'loaded'; view: ApprovalView }
  | { kind: 'error'; error: ApprovalLoadError };

type DecisionState =
  | { kind: 'idle' }
  | { kind: 'saving' }
  | { kind: 'done'; status: Approval['status']; repeated: boolean }
  | { kind: 'conflict' }
  | { kind: 'failed'; message: string };

/**
 * One approval: the Tool, the committed call arguments, where it sits, and the approve or
 * reject controls. The Backend settles the decision; this page only sends it and then shows
 * what was recorded, including a different decision that got there first.
 */
export function ApprovalDetailPage() {
  const copy = useCopy();
  const a = copy.approvals;
  const { approvalId = '' } = useParams();
  const token = useOperatorStore((s) => s.token);
  const [state, setState] = useState<LoadState>({ kind: 'loading' });
  const [decision, setDecision] = useState<DecisionState>({ kind: 'idle' });
  const [reason, setReason] = useState('');
  const [reasonError, setReasonError] = useState<string | null>(null);
  const [reloads, setReloads] = useState(0);

  useDocumentTitle(copy.pageTitles.approval(approvalId.slice(0, 8)));

  useEffect(() => {
    if (!token || !approvalId) return;
    const controller = new AbortController();
    getApproval(approvalId, token, controller.signal)
      .then((view) => {
        setState({ kind: 'loaded', view });
      })
      .catch((error: unknown) => {
        if (controller.signal.aborted) return;
        setState({ kind: 'error', error: classifyApprovalError(error, false) });
      });
    return () => {
      controller.abort();
    };
  }, [token, approvalId, reloads]);

  const reload = () => {
    setState({ kind: 'loading' });
    setReloads((n) => n + 1);
  };
  const tokenField = <OperatorTokenField onChange={reload} />;

  const decide = (kind: 'APPROVE' | 'REJECT') => {
    const text = reason.trim();
    if (kind === 'REJECT' && !text) {
      setReasonError(a.reasonRequired);
      return;
    }
    setReasonError(null);
    setDecision({ kind: 'saving' });
    decideApproval(approvalId, text ? { decision: kind, reason: text } : { decision: kind }, token)
      .then((res) => {
        setDecision({ kind: 'done', status: res.approval.status, repeated: res.repeated });
        setState((prev) =>
          prev.kind === 'loaded'
            ? { kind: 'loaded', view: { ...prev.view, ...res.approval } }
            : prev,
        );
      })
      .catch((error: unknown) => {
        if (error instanceof ApiRequestError && error.status === 409) {
          // Someone else's decision was recorded first: show what the Backend holds.
          setDecision({ kind: 'conflict' });
          reload();
          return;
        }
        setDecision({
          kind: 'failed',
          message: approvalErrorText(classifyApprovalError(error, false)),
        });
      });
  };

  const crumbs = [
    { label: a.title, to: listHref('approvals', true) },
    { label: copy.pageTitles.approval(approvalId.slice(0, 8)) },
  ];

  let body;
  if (!token) {
    body = (
      <div className="flex flex-col gap-4">
        <p className="m-0 text-md text-fg-secondary">{a.tokenRequired}</p>
        {tokenField}
      </div>
    );
  } else if (state.kind === 'loading') {
    body = <Skeleton kind="text" count={6} />;
  } else if (state.kind === 'error') {
    body = (
      <StateBlock
        kind={state.error.kind === 'notFound' ? 'notfound' : 'error'}
        title={approvalErrorText(state.error)}
        code={
          state.error.kind === 'error'
            ? copy.states.errorCode(state.error.status, state.error.code)
            : undefined
        }
        action={
          state.error.kind === 'notFound'
            ? { label: copy.nav.backToApprovals, to: listHref('approvals', true) }
            : { label: copy.retry, onClick: reload }
        }
        details={state.error.kind === 'unauthorized' ? tokenField : undefined}
      />
    );
  } else {
    const view = state.view;
    const pending = view.status === 'PENDING';
    body = (
      <div className="flex flex-col gap-6">
        <section className="flex flex-col gap-3 rounded-lg border border-border bg-surface p-4">
          <div className="flex items-center gap-3">
            <h2 className="m-0 font-mono text-lg font-semibold">{view.toolName}</h2>
            <StatusIndicator status={{ kind: 'approval', value: view.status }} colourLabel />
          </div>
          <FactList
            compact
            facts={[
              { label: a.requestedAt, value: <Timestamp iso={view.requestedAt} /> },
              {
                label: pending
                  ? a.expiresAt
                  : view.status === 'EXPIRED'
                    ? a.expiredAt
                    : a.expiresAt,
                value: (
                  <span className="flex flex-col">
                    <Timestamp iso={view.deadline} />
                    {pending ? (
                      <span className="text-fg-muted">
                        <Deadline iso={view.deadline} />
                      </span>
                    ) : null}
                  </span>
                ),
              },
              view.decidedAt
                ? { label: a.decidedAt, value: <Timestamp iso={view.decidedAt} /> }
                : null,
              view.decidedBy ? { label: a.decidedBy, value: view.decidedBy } : null,
              view.reason ? { label: a.reason, value: view.reason } : null,
              {
                label: a.run,
                value: (
                  <IdChip id={view.runId} label={a.run.toLowerCase()} to={`/runs/${view.runId}`} />
                ),
              },
              {
                label: a.workflow,
                value: (
                  <IdChip
                    id={view.workflowId}
                    label={a.workflow.toLowerCase()}
                    to={`/workflows/${view.workflowId}`}
                  />
                ),
              },
              {
                label: a.agentRun,
                value: <IdChip id={view.agentRunId} label={a.agentRun.toLowerCase()} />,
              },
              {
                label: a.action,
                value: <IdChip id={view.actionId} label={a.action.toLowerCase()} />,
              },
            ]}
          />
          {pending ? <p className="m-0 text-md text-fg-secondary">{a.pauseNote}</p> : null}
        </section>

        <section className="flex flex-col gap-2 rounded-lg border border-border bg-surface p-4">
          <h3 className="m-0 text-md font-semibold">{a.arguments}</h3>
          {view.arguments === null ? (
            <p className="m-0 text-md text-fg-muted">{a.noArguments}</p>
          ) : (
            <JsonViewer value={view.arguments} root="arguments" />
          )}
        </section>

        {decision.kind === 'done' ? (
          <Callout tone="neutral">
            {decision.status === 'APPROVED' ? a.approved : a.rejected}
            {decision.repeated ? ` ${a.repeated}` : ''}
          </Callout>
        ) : null}
        {decision.kind === 'conflict' ? <Callout tone="caution">{a.conflict}</Callout> : null}
        {decision.kind === 'failed' ? <Callout tone="danger">{decision.message}</Callout> : null}

        {pending && decision.kind !== 'done' ? (
          <section className="flex flex-col gap-4 rounded-lg border border-border bg-surface p-4">
            <Field label={a.reasonLabel} hint={a.reasonHelp} error={reasonError}>
              {(control) => (
                <TextArea
                  {...control}
                  rows={3}
                  value={reason}
                  placeholder={a.reasonPlaceholder}
                  onChange={(e) => {
                    setReason(e.target.value);
                    if (reasonError) setReasonError(null);
                  }}
                />
              )}
            </Field>
            <div className="flex gap-3">
              <Button
                onClick={() => {
                  decide('APPROVE');
                }}
                disabled={decision.kind === 'saving'}
              >
                {a.approve}
              </Button>
              <Button
                variant="secondary"
                onClick={() => {
                  decide('REJECT');
                }}
                disabled={decision.kind === 'saving'}
              >
                {a.reject}
              </Button>
              {decision.kind === 'saving' ? (
                <span role="status" className="self-center text-md text-fg-muted">
                  {a.deciding}
                </span>
              ) : null}
            </div>
          </section>
        ) : null}
        {!pending && decision.kind === 'idle' ? (
          <p className="m-0 text-md text-fg-muted">{a.settled}</p>
        ) : null}
      </div>
    );
  }

  return (
    <>
      <PageHeader title={crumbs} />
      <main style={{ maxWidth: 960 }} className="mx-auto w-full flex-1 px-8 pt-8 pb-12">
        {body}
      </main>
    </>
  );
}
