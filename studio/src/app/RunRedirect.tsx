import { useEffect, useState } from 'react';
import { useLocation, useNavigate, useParams } from 'react-router';

import { ApiRequestError, getRun } from '@/api/client';
import { NotFoundView } from '@/components/shell/NotFoundView';
import { PageHeader } from '@/components/shell/PageHeader';
import { Skeleton, StateBlock } from '@/components/state/StateBlock';
import { useCopy } from '@/lib/copy';
import { FROM_PARAM } from '@/lib/nav/list-state';
import { shortCode } from '@/lib/short-code';

type State = { kind: 'loading' } | { kind: 'missing' } | { kind: 'error'; code: string | null };

/**
 * A run's own address. A run always opens in its workflow's Runs tab, so this reads the
 * run to learn its workflow and replaces the address with that page, marked as entered
 * from Runs.
 */
export function RunRedirect() {
  const { runId = '' } = useParams();
  const { search, hash } = useLocation();
  const navigate = useNavigate();
  const copy = useCopy();
  const [state, setState] = useState<State>({ kind: 'loading' });
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    const controller = new AbortController();
    getRun(runId, controller.signal).then(
      (snapshot) => {
        const q = new URLSearchParams(search);
        q.set(FROM_PARAM, 'runs');
        const workflow = encodeURIComponent(snapshot.run.workflowId);
        void navigate(
          `/workflows/${workflow}/runs/${encodeURIComponent(runId)}?${q.toString()}${hash}`,
          { replace: true },
        );
      },
      (error: unknown) => {
        if (controller.signal.aborted) return;
        if (error instanceof ApiRequestError && error.status === 404) setState({ kind: 'missing' });
        else
          setState({
            kind: 'error',
            code:
              error instanceof ApiRequestError
                ? copy.states.errorCode(error.status, error.code)
                : null,
          });
      },
    );
    return () => {
      controller.abort();
    };
  }, [runId, search, hash, navigate, attempt, copy]);

  if (state.kind === 'missing') return <NotFoundView kind="run" id={runId} />;
  return (
    <>
      <PageHeader title={[{ label: copy.nav.runs, to: '/runs' }, { label: shortCode(runId) }]} />
      <main className="flex flex-1 flex-col px-8 py-6">
        {state.kind === 'loading' ? (
          <Skeleton kind="text" count={3} />
        ) : (
          <StateBlock
            kind="error"
            title={copy.states.loadFailed(copy.states.what.run)}
            body={copy.states.errorBody}
            code={state.code ?? undefined}
            action={{
              label: copy.retry,
              primary: true,
              onClick: () => {
                setState({ kind: 'loading' });
                setAttempt((n) => n + 1);
              },
            }}
          />
        )}
      </main>
    </>
  );
}
