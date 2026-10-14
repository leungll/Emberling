import type { ReactNode } from 'react';

import { Kbd } from '@/components/ui/kbd';

/** A tooltip that names an action and its key. */
export function keyTip(label: string, key?: string): ReactNode {
  if (!key) return label;
  return (
    <span className="inline-flex items-center gap-2">
      {label}
      <Kbd>{key}</Kbd>
    </span>
  );
}
