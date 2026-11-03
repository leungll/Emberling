import { ApiRequestError } from '@/api/client';
import { copy } from '@/lib/copy';

export type ApprovalLoadError =
  | { kind: 'unauthorized' }
  | { kind: 'notConfigured' }
  | { kind: 'notFound' }
  | { kind: 'error'; status: number; code: string };

/**
 * What an approval request's failure means to the viewer. A 401 is the credential; a 404
 * on the list is a server without the operator routes, since they are registered only
 * when a credential is configured; a 404 on one approval is that approval.
 */
export function classifyApprovalError(error: unknown, onList: boolean): ApprovalLoadError {
  if (error instanceof ApiRequestError) {
    if (error.status === 401) return { kind: 'unauthorized' };
    if (error.status === 404) return onList ? { kind: 'notConfigured' } : { kind: 'notFound' };
    return { kind: 'error', status: error.status, code: error.code };
  }
  return { kind: 'error', status: 503, code: 'DEPENDENCY_UNAVAILABLE' };
}

export function approvalErrorText(error: ApprovalLoadError): string {
  const a = copy.approvals;
  switch (error.kind) {
    case 'unauthorized':
      return a.unauthorized;
    case 'notConfigured':
      return a.notConfigured;
    case 'notFound':
      return a.notFound;
    case 'error':
      return copy.states.loadFailed(a.title.toLowerCase());
  }
}
