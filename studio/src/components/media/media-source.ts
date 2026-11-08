import { createContext, useContext } from 'react';

import type { AgentTrace, AgentTraceMedia } from '@/api/agent-types';
import type { MediaResolutionContext } from './media-ref';

/**
 * The run whose media a page shows: the media its Agent Traces list and its input as
 * submitted. Every media-typed value (JSON Schema `contentMediaType`) is resolved against it
 * through one resolver; without a run nothing resolves and the value shows as the reference
 * it is.
 */
export const MediaSourceContext = createContext<MediaResolutionContext>({});

export function useMediaSource(): MediaResolutionContext {
  return useContext(MediaSourceContext);
}

/**
 * The media every Agent Trace of a run lists, as one list; truncated when any of them is. A
 * trace served without the list (an older Backend) contributes nothing: a page never fails
 * to render because one trace lacks it.
 */
export function mediaOfTraces(traces: Iterable<AgentTrace>): AgentTraceMedia {
  const out: AgentTraceMedia = { items: [], truncated: false };
  for (const trace of traces) {
    const media: AgentTraceMedia | undefined = trace.media;
    if (!media) continue;
    out.items.push(...media.items);
    out.truncated = out.truncated || media.truncated;
  }
  return out;
}
