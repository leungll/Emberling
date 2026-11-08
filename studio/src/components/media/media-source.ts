import { createContext, useContext } from 'react';

import type { MediaUrlContext } from './media-ref';

/**
 * The run whose media a page shows. Every media-typed value (JSON Schema `contentMediaType`)
 * is resolved against it through one resolver; without a run only nothing resolves and the
 * value shows as the reference it is.
 */
export const MediaSourceContext = createContext<MediaUrlContext>({ runId: null, runInput: null });

export function useMediaSource(): MediaUrlContext {
  return useContext(MediaSourceContext);
}
