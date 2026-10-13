import { useEffect } from 'react';
import { create } from 'zustand';

import type { ShortcutGroup } from '@/lib/shortcuts';

/**
 * The keyboard shortcut sheet: whether it is open, and the groups the current page has
 * registered. Every page opens the same sheet with "?".
 */
interface ShortcutsState {
  open: boolean;
  groups: readonly ShortcutGroup[];
  setOpen: (open: boolean) => void;
  setGroups: (groups: readonly ShortcutGroup[]) => void;
}

export const useShortcutsStore = create<ShortcutsState>((set) => ({
  open: false,
  groups: [],
  setOpen: (open) => {
    set({ open });
  },
  setGroups: (groups) => {
    set({ groups });
  },
}));

/** Registers the current page's shortcut groups for the sheet while the page is shown. */
export function useShortcutGroups(groups: readonly ShortcutGroup[]): void {
  const setGroups = useShortcutsStore((s) => s.setGroups);
  const signature = JSON.stringify(groups);
  useEffect(() => {
    setGroups(JSON.parse(signature) as ShortcutGroup[]);
    return () => {
      setGroups([]);
    };
  }, [signature, setGroups]);
}
