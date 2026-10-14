import { useEffect, useRef } from 'react';

import { copy } from '@/lib/copy';
import { modalOpen } from '@/lib/shortcuts';
import { useShortcutGroups } from '@/stores/shortcuts-store';

/**
 * The Editor's workflow keys, ⌘ S to save and ⌘ ↵ to run, which also work while typing in a
 * field; and the Editor's rows of the shortcut sheet.
 */
export function useEditorShortcuts({
  save,
  run,
  enabled,
}: {
  save: () => void;
  run: () => void;
  enabled: boolean;
}) {
  const latest = useRef({ save, run });
  useEffect(() => {
    latest.current = { save, run };
  });
  useEffect(() => {
    if (!enabled) return;
    const onKey = (e: KeyboardEvent) => {
      if (!(e.metaKey || e.ctrlKey) || e.altKey || e.shiftKey || modalOpen()) return;
      if (e.key.toLowerCase() === 's') {
        e.preventDefault();
        latest.current.save();
      } else if (e.key === 'Enter') {
        e.preventDefault();
        latest.current.run();
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [enabled]);

  const s = copy.editor.shortcuts;
  useShortcutGroups([
    {
      title: s.canvas,
      rows: [
        { keys: ['↑', '↓', '←', '→'], description: s.arrows },
        { keys: ['↵'], description: s.enter },
        { keys: ['Esc'], description: s.esc },
        { keys: ['⌫'], description: s.del },
        { keys: ['⌘ D'], description: s.dup },
        { keys: ['⌘ C'], description: s.copy },
        { keys: ['⌘ X'], description: s.cut },
        { keys: ['⌘ V'], description: s.paste },
        { keys: ['⌥ ↑', '⌥ ↓', '⌥ ←', '⌥ →'], description: s.nudge },
        { keys: ['⇧ F10'], description: s.menu },
        { keys: ['⌘ A'], description: s.all },
      ],
    },
    {
      title: s.view,
      rows: [
        { keys: ['V'], description: s.selectTool },
        { keys: ['H'], description: s.panTool },
        { keys: ['Space'], description: s.space },
        { keys: ['+'], description: s.zoomIn },
        { keys: ['−'], description: s.zoomOut },
        { keys: ['⇧ 1'], description: s.fit },
        { keys: ['⇧ 0'], description: s.reset },
      ],
    },
    {
      title: s.workflow,
      rows: [
        { keys: ['⌘ S'], description: s.save },
        { keys: ['⌘ ↵'], description: s.run },
        { keys: ['⌘ Z'], description: s.undo },
        { keys: ['⇧ ⌘ Z'], description: s.redo },
        { keys: ['/'], description: s.search },
        { keys: ['?'], description: s.sheet },
      ],
    },
  ]);
}
