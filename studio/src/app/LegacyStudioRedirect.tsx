import { Navigate, useLocation, useParams } from 'react-router';

/** Addresses from before the Workflows section keep working: /studio/… is /workflows/…. */
export function LegacyStudioRedirect() {
  const { workflowId } = useParams();
  const location = useLocation();
  const { search, hash } = location;
  const state: unknown = location.state;
  const path = workflowId ? `/workflows/${encodeURIComponent(workflowId)}` : '/workflows/new';
  return <Navigate to={{ pathname: path, search, hash }} state={state} replace />;
}
