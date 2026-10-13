import { Tabs as RTabs } from 'radix-ui';
import type { ReactNode } from 'react';

import { cn } from '@/lib/utils';

export interface TabItem<V extends string> {
  value: V;
  label: string;
  /** A count shown beside the label, such as the number of failed runs. */
  count?: number;
}

interface TabsProps<V extends string> {
  label: string;
  value: V;
  items: readonly TabItem<V>[];
  onValueChange: (value: V) => void;
  /** The content of the selected tab. */
  children: ReactNode;
  /** 32 px items on a list card; 28 px elsewhere. */
  size?: 'sm' | 'md';
  className?: string;
  listClassName?: string;
}

/**
 * Underline tabs: filters of one list or sections of one object. One Tab stop; ←/→ move
 * and select. Only the selected tab's content is rendered.
 */
export function Tabs<V extends string>({
  label,
  value,
  items,
  onValueChange,
  children,
  size = 'sm',
  className,
  listClassName,
}: TabsProps<V>) {
  return (
    <RTabs.Root
      value={value}
      onValueChange={(v) => {
        onValueChange(v as V);
      }}
      activationMode="automatic"
      className={className}
    >
      <RTabs.List
        aria-label={label}
        className={cn(
          'relative flex items-end gap-4 before:absolute before:inset-x-0 before:bottom-0 before:h-px before:bg-border-subtle',
          listClassName,
        )}
      >
        {items.map((item) => (
          <RTabs.Trigger
            key={item.value}
            value={item.value}
            className={cn(
              'relative z-local inline-flex items-center gap-1.5 border-b-2 border-transparent text-sm font-medium whitespace-nowrap text-fg-muted hover:text-fg focus-visible:outline-offset-0',
              'data-[state=active]:border-accent data-[state=active]:text-fg',
              size === 'md' ? 'h-control-md' : 'h-control-sm',
            )}
          >
            {item.label}
            {item.count === undefined ? null : ' '}
            {item.count === undefined ? null : (
              <span className="text-fg-muted tabular-nums font-normal">
                {item.count.toLocaleString('en-US')}
              </span>
            )}
          </RTabs.Trigger>
        ))}
      </RTabs.List>
      <RTabs.Content value={value} className="focus-visible:outline-none" tabIndex={-1}>
        {children}
      </RTabs.Content>
    </RTabs.Root>
  );
}
