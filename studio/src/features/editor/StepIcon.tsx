import {
  ArrowRightFromLine,
  ArrowRightToLine,
  Braces,
  Sparkles,
  type LucideIcon,
} from 'lucide-react';
import { createElement } from 'react';

import type { GroupId } from './model/steps';
import { StepIcon as TypeIcon } from '@/components/step/StepIcon';

const GROUP_ICON: Record<GroupId, LucideIcon> = {
  inputs: ArrowRightToLine,
  ai: Sparkles,
  transform: Braces,
  outputs: ArrowRightFromLine,
};

/** A step's glyph in the Editor; a type without its own glyph gets its group's. */
export function StepIcon({
  type,
  group,
  className,
}: {
  type: string;
  group?: GroupId;
  className?: string;
}) {
  return (
    <TypeIcon type={type} fallback={group ? GROUP_ICON[group] : undefined} className={className} />
  );
}

export function GroupIcon({ group, className }: { group: GroupId; className?: string }) {
  return createElement(GROUP_ICON[group], {
    'aria-hidden': true,
    className: className ?? 'size-icon-sm',
  });
}
