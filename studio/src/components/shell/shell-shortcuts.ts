import { useCopy } from '@/lib/copy';
import type { ShortcutGroup } from '@/lib/shortcuts';
import { useShortcutGroups } from '@/stores/shortcuts-store';

/** The shortcuts every list page has; a list page adds its own group after these. */
export function useShellShortcutGroups(extra: readonly ShortcutGroup[] = []): void {
  const copy = useCopy();
  const s = copy.shortcuts;
  useShortcutGroups([
    {
      title: s.groups.everywhere,
      rows: [
        { keys: ['?'], description: s.openSheet },
        { keys: ['['], description: s.toggleSidebar },
      ],
    },
    ...extra,
  ]);
}
