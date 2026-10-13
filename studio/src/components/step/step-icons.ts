import {
  ArrowRightFromLine,
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

/**
 * One distinct glyph per registered step type. The Editor's cards and sidebar and the run
 * graph and details use the same map, so a step in a run reads like the same step in the
 * Editor.
 */
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

/** The glyph of a step type; an unknown type gets the fallback. */
export function stepIcon(type: string, fallback: LucideIcon = Workflow): LucideIcon {
  return TYPE_ICON[type] ?? fallback;
}
