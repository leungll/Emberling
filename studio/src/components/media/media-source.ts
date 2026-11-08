import { createContext, useContext } from 'react';

import type { AgentTrace } from '@/api/agent-types';
import type { RunMedia } from '@/api/types';
import type { MediaResolutionContext, ServedMedia, ServedMediaItem } from './media-ref';

/**
 * The run whose media a page shows: the media the Run and its Agent Traces list and its
 * input as submitted. Every media-typed value (JSON Schema `contentMediaType`) and every
 * value equal to a served reference is resolved against it through one resolver; without a
 * run nothing resolves and the value shows as the reference it is.
 */
export const MediaSourceContext = createContext<MediaResolutionContext>({});

export function useMediaSource(): MediaResolutionContext {
  return useContext(MediaSourceContext);
}

/**
 * The media a run serves, as one list: the Run's own list (its steps' outputs, the input's
 * source among them) first, then each Agent Trace's list in step order; truncated when any
 * of them is. A list a Backend did not serve contributes nothing: a page never fails to
 * render because the Run or one trace lacks it.
 */
export function mediaOfRun(
  run: RunMedia | null | undefined,
  traces: Iterable<AgentTrace>,
): ServedMedia {
  const items: ServedMediaItem[] = [...(run?.items ?? [])];
  let truncated = run?.truncated ?? false;
  for (const trace of traces) {
    const media: ServedMedia | undefined = trace.media;
    if (!media) continue;
    items.push(...media.items);
    truncated = truncated || media.truncated;
  }
  return { items, truncated };
}

/** The media every Agent Trace lists, without a Run list. */
export function mediaOfTraces(traces: Iterable<AgentTrace>): ServedMedia {
  return mediaOfRun(null, traces);
}
