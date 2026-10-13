import {
  ArrowRightFromLine,
  Bot,
  Braces,
  ImagePlus,
  ImageUp,
  Images,
  Layers,
  Monitor,
  Sparkle,
  Type,
  Workflow,
  type LucideIcon,
} from 'lucide-react';

/** The step icons of the Editor's cards, so a run's step reads like the same step there. */
const NODE_ICON: Record<string, LucideIcon> = {
  text_input: Type,
  image_input: ImageUp,
  media_brief: Images,
  prompt_template: Braces,
  text_generation: Sparkle,
  image_generation: ImagePlus,
  agent: Bot,
  text_output: ArrowRightFromLine,
  media_output: Monitor,
  media_result: Layers,
};

export function nodeIcon(type: string): LucideIcon {
  return NODE_ICON[type] ?? Workflow;
}
