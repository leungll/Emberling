import { useMemo, type ReactNode } from 'react';

import { MediaSourceContext, mediaOfTraces } from '@/components/media/media-source';
import { RunImagesContext } from '@/components/media/value-media';
import type { RunModel } from './run-model';
import { runImagesOf } from './run-value-media';

/**
 * Gives every value shown inside the run's images and its media source: the run's records
 * name its images, and the one media resolver places anything a schema marks as media
 * through the media its Agent Traces list and the assets its input names.
 */
export function RunMediaProvider({ model, children }: { model: RunModel; children: ReactNode }) {
  const run = model.snapshot.run;
  const source = useMemo(
    () => ({
      media: mediaOfTraces(model.steps.flatMap((s) => (s.trace ? [s.trace] : []))),
      runInput: run.input,
    }),
    [model.steps, run.input],
  );
  return (
    <RunImagesContext.Provider value={runImagesOf(model)}>
      <MediaSourceContext.Provider value={source}>{children}</MediaSourceContext.Provider>
    </RunImagesContext.Provider>
  );
}
