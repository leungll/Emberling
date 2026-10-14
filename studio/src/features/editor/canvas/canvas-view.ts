import { createContext, useContext } from 'react';

import type { PortDataType } from '@/api/types';
import type { PlaceMap } from '@/features/editor/model/layout';
import type { ZoomLevel } from '@/features/editor/model/view';

/** A link being dragged from an output, so inputs can show whether they would take it. */
export interface Dragging {
  sid: string;
  sh: string;
  type: PortDataType;
}

/** How the canvas draws its cards right now. Changes with the zoom, not with the draft. */
export interface CanvasView {
  level: ZoomLevel;
  zoom: number;
  /** One-row card height on the canvas below the full-card zoom. */
  lodH: number;
  collapsed: ReadonlySet<string>;
  toggleCollapsed: (id: string) => void;
  /** The card that is the canvas's one Tab stop. */
  tabStop: string | null;
  places: PlaceMap;
  /** The pan tool is on: cards and ports don't react to the pointer. */
  panning: boolean;
  dragging: Dragging | null;
  /** Asks the canvas to measure a card's port rows again. */
  remeasure: (id: string) => void;
  onRenameEnd: () => void;
}

export const CanvasViewContext = createContext<CanvasView | null>(null);

export function useCanvasView(): CanvasView {
  const v = useContext(CanvasViewContext);
  if (!v) throw new Error('useCanvasView outside the canvas');
  return v;
}

/** A port's vertical place on a one-row or collapsed card: spread 16 px apart around the middle. */
export function portsY(h: number, count: number, i: number): number {
  return h / 2 + (i - (count - 1) / 2) * 16;
}

/** Which links show their + and × right now: the hovered one and the one a step goes into. */
export interface LinkTools {
  hover: string | null;
  setHover: (id: string | null) => void;
  /** The link a step is being inserted into, while the add menu is open. */
  inserting: string | null;
  /** Tools stay hidden while a card or a link is dragged, and on the pan tool. */
  hidden: boolean;
}

export const LinkToolsContext = createContext<LinkTools>({
  hover: null,
  setHover: () => undefined,
  inserting: null,
  hidden: false,
});

/** The drag data type of a step dragged from the Steps sidebar. */
export const STEP_MIME = 'application/x-emb-step';
