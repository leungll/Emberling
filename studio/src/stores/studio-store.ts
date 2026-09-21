import { create } from 'zustand';

/**
 * Client interaction state only.
 *
 * Nothing here is an execution fact. Run status, NodeRun status, Attempts, Turns and
 * Events come from the Backend Snapshot and the SSE stream and are held as they arrive.
 * Run aggregation, transition rules and retry decisions belong to the Backend; adding
 * them here would create a second, divergent execution model.
 */

export type StudioMode = 'edit' | 'observe';

interface StudioState {
  mode: StudioMode;
  /** Definition node id in edit mode, NodeRun id in observe mode. */
  selectedNodeId: string | null;
  selectedEventSeq: number | null;
  /** Timeline follows the newest Event until the user picks a historical one. */
  followLive: boolean;
  hasUnsavedChanges: boolean;

  setMode: (mode: StudioMode) => void;
  selectNode: (nodeId: string | null) => void;
  selectEvent: (seq: number | null) => void;
  setFollowLive: (followLive: boolean) => void;
  setUnsavedChanges: (hasUnsavedChanges: boolean) => void;
  resetSelection: () => void;
}

export const useStudioStore = create<StudioState>((set) => ({
  mode: 'edit',
  selectedNodeId: null,
  selectedEventSeq: null,
  followLive: true,
  hasUnsavedChanges: false,

  setMode: (mode) => set({ mode, selectedNodeId: null, selectedEventSeq: null }),
  selectNode: (selectedNodeId) => set({ selectedNodeId }),
  // Picking a historical Event stops the timeline from jumping; Resume Live restores it.
  selectEvent: (selectedEventSeq) =>
    set(
      selectedEventSeq === null
        ? { selectedEventSeq: null }
        : { selectedEventSeq, followLive: false },
    ),
  setFollowLive: (followLive) =>
    set(followLive ? { followLive: true, selectedEventSeq: null } : { followLive: false }),
  setUnsavedChanges: (hasUnsavedChanges) => set({ hasUnsavedChanges }),
  resetSelection: () => set({ selectedNodeId: null, selectedEventSeq: null }),
}));
