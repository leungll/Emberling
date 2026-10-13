import {
  ArrowRightFromLine,
  ArrowRightToLine,
  Bot,
  Braces,
  ImagePlus,
  ImageUp,
  Images,
  Layers,
  Monitor,
  Sparkles,
  Type,
  Workflow,
  type LucideIcon,
} from 'lucide-react';
import { createElement } from 'react';

import type { GroupId } from './model/steps';

/** One distinct glyph per step type; an unknown type gets its group's glyph. */
const TYPE_ICON: Record<string, LucideIcon> = {
  text_input: Type,
  image_input: ImageUp,
  media_brief: Images,
  prompt_template: Braces,
  text_generation: Sparkles,
  image_generation: ImagePlus,
  agent: Bot,
  text_output: ArrowRightFromLine,
  media_output: Monitor,
  media_result: Layers,
};

const GROUP_ICON: Record<GroupId, LucideIcon> = {
  inputs: ArrowRightToLine,
  ai: Sparkles,
  transform: Braces,
  outputs: ArrowRightFromLine,
};

function stepIcon(type: string, group?: GroupId): LucideIcon {
  return TYPE_ICON[type] ?? (group ? GROUP_ICON[group] : Workflow);
}

export function StepIcon({
  type,
  group,
  className,
}: {
  type: string;
  group?: GroupId;
  className?: string;
}) {
  return createElement(stepIcon(type, group), {
    'aria-hidden': true,
    className: className ?? 'size-icon-sm',
  });
}

export function GroupIcon({ group, className }: { group: GroupId; className?: string }) {
  return createElement(GROUP_ICON[group], {
    'aria-hidden': true,
    className: className ?? 'size-icon-sm',
  });
}
