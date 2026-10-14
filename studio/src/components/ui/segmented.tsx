import { RadioGroup } from 'radix-ui';
import type { ReactNode } from 'react';

import { cn } from '@/lib/utils';

export interface SegmentedOption<V extends string> {
  value: V;
  label: ReactNode;
}

interface SegmentedProps<V extends string> {
  /** Names the group for screen readers, e.g. "Show as". */
  label: string;
  value: V;
  options: readonly SegmentedOption<V>[];
  onValueChange: (value: V) => void;
  className?: string;
}

const ITEM =
  'inline-flex h-control-sm items-center rounded-md border border-transparent px-3 text-sm font-medium whitespace-nowrap text-fg-muted focus-visible:-outline-offset-2 hover:text-fg';
const SELECTED =
  'data-[state=checked]:bg-surface data-[state=checked]:text-fg data-[state=checked]:border-border-control';

/**
 * Two to four views of the same thing (Tree | Raw, Fields | JSON). One Tab stop; ←/→ move
 * and select.
 */
export function Segmented<V extends string>({
  label,
  value,
  options,
  onValueChange,
  className,
}: SegmentedProps<V>) {
  return (
    <RadioGroup.Root
      aria-label={label}
      value={value}
      onValueChange={(v) => {
        onValueChange(v as V);
      }}
      orientation="horizontal"
      loop
      className={cn(
        'inline-flex w-fit items-center rounded-md bg-surface-hover outline outline-1 -outline-offset-1 outline-border',
        className,
      )}
    >
      {options.map((o) => (
        <RadioGroup.Item key={o.value} value={o.value} className={cn(ITEM, SELECTED)}>
          {o.label}
        </RadioGroup.Item>
      ))}
    </RadioGroup.Root>
  );
}
