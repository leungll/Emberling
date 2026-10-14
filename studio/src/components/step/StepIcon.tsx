import type { LucideIcon } from 'lucide-react';
import { createElement } from 'react';

import { stepIcon } from './step-icons';

export function StepIcon({
  type,
  fallback,
  className,
}: {
  type: string;
  fallback?: LucideIcon;
  className?: string;
}) {
  return createElement(stepIcon(type, fallback), {
    'aria-hidden': true,
    className: className ?? 'size-icon-sm',
  });
}
