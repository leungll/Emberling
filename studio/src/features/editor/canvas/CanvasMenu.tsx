import { ClipboardPaste, Copy, CopyPlus, Pencil, Plus, Scan, Trash2 } from 'lucide-react';

import { Menu, MenuContent, MenuItem, MenuSeparator, MenuTrigger } from '@/components/ui/menu';
import { useCopy } from '@/lib/copy';

export type MenuTarget =
  | { kind: 'step'; ids: readonly string[] }
  | { kind: 'link'; edgeId: string }
  | { kind: 'canvas'; canPaste: boolean };

export interface MenuRequest {
  x: number;
  y: number;
  target: MenuTarget;
  /** Opened from the keyboard: the first item takes focus. */
  keyboard: boolean;
}

export type MenuAction =
  'settings' | 'duplicate' | 'copy' | 'delete' | 'insert' | 'deleteLink' | 'add' | 'paste' | 'fit';

/** The canvas context menu, opened at the pointer or (Shift F10) at the focused card. */
export function CanvasMenu({
  request,
  onAction,
  onClose,
}: {
  request: MenuRequest;
  onAction: (action: MenuAction) => void;
  onClose: () => void;
}) {
  const copy = useCopy();
  const t = copy.editor.menu;
  const target = request.target;
  const many = target.kind === 'step' && target.ids.length > 1 ? target.ids.length : 0;
  const label = target.kind === 'step' ? t.step : target.kind === 'link' ? t.link : t.canvas;
  return (
    <Menu
      open
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <MenuTrigger asChild>
        <span
          aria-hidden="true"
          style={{ position: 'fixed', left: request.x, top: request.y, width: 0, height: 0 }}
        />
      </MenuTrigger>
      <MenuContent align="start" label={label}>
        {target.kind === 'step' ? (
          <>
            {many ? null : (
              <MenuItem
                icon={<Pencil aria-hidden="true" />}
                shortcut="↵"
                onSelect={() => onAction('settings')}
              >
                {t.openSettings}
              </MenuItem>
            )}
            <MenuItem
              icon={<CopyPlus aria-hidden="true" />}
              shortcut="⌘ D"
              onSelect={() => onAction('duplicate')}
            >
              {many ? t.duplicateMany(many) : t.duplicate}
            </MenuItem>
            <MenuItem
              icon={<Copy aria-hidden="true" />}
              shortcut="⌘ C"
              onSelect={() => onAction('copy')}
            >
              {many ? t.copyMany(many) : t.copy}
            </MenuItem>
            <MenuSeparator />
            <MenuItem
              icon={<Trash2 aria-hidden="true" />}
              shortcut="⌫"
              danger
              onSelect={() => onAction('delete')}
            >
              {many ? t.deleteMany(many) : t.delete}
            </MenuItem>
          </>
        ) : target.kind === 'link' ? (
          <>
            <MenuItem icon={<Plus aria-hidden="true" />} onSelect={() => onAction('insert')}>
              {t.insert}
            </MenuItem>
            <MenuSeparator />
            <MenuItem
              icon={<Trash2 aria-hidden="true" />}
              shortcut="⌫"
              danger
              onSelect={() => onAction('deleteLink')}
            >
              {t.deleteLink}
            </MenuItem>
          </>
        ) : (
          <>
            <MenuItem icon={<Plus aria-hidden="true" />} onSelect={() => onAction('add')}>
              {t.addHere}
            </MenuItem>
            {target.canPaste ? (
              <MenuItem
                icon={<ClipboardPaste aria-hidden="true" />}
                shortcut="⌘ V"
                onSelect={() => onAction('paste')}
              >
                {t.pasteHere}
              </MenuItem>
            ) : null}
            <MenuItem
              icon={<Scan aria-hidden="true" />}
              shortcut="⇧ 1"
              onSelect={() => onAction('fit')}
            >
              {t.fit}
            </MenuItem>
          </>
        )}
      </MenuContent>
    </Menu>
  );
}
