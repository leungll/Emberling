import { useMemo, type ReactNode } from 'react';

import { MediaSourceContext } from '@/components/media/media-source';
import { RunImagesContext } from '@/components/media/value-media';
import type { RunModel } from './run-model';
import { runImagesOf } from './run-value-media';

/**
 * Gives every value shown inside the run's images and its media source: the run's records
 * name its images, and the one media resolver places anything a schema marks as media.
 */
export function RunMediaProvider({ model, children }: { model: RunModel; children: ReactNode }) {
  const run = model.snapshot.run;
  const source = useMemo(() => ({ runId: run.id, runInput: run.input }), [run.id, run.input]);
  return (
    <RunImagesContext.Provider value={runImagesOf(model)}>
      <MediaSourceContext.Provider value={source}>{children}</MediaSourceContext.Provider>
    </RunImagesContext.Provider>
  );
}
