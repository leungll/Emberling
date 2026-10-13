import { useState } from 'react';
import type { Blocker } from 'react-router';

import { Button } from '@/components/ui/button';
import { Dialog } from '@/components/ui/dialog';
import { useCopy } from '@/lib/copy';

interface LeaveGuardDialogProps {
  blocker: Blocker;
  /** Changes with issues cannot be saved, so only Leave and Stay are offered. */
  hasIssues: boolean;
  /** Saves the changes; resolves true when they were saved. */
  onSaveAndLeave: () => Promise<boolean>;
}

/**
 * "Leave without saving?": Leave (ghost), Stay (secondary) and Save and leave (primary,
 * initial focus). With issues, Stay is the primary choice and there is no Save and leave.
 */
export function LeaveGuardDialog({ blocker, hasIssues, onSaveAndLeave }: LeaveGuardDialogProps) {
  const copy = useCopy();
  const g = copy.leaveGuard;
  const [saving, setSaving] = useState(false);
  const open = blocker.state === 'blocked';

  const stay = () => {
    if (blocker.state === 'blocked') blocker.reset();
  };
  const leave = () => {
    if (blocker.state === 'blocked') blocker.proceed();
  };

  return (
    <Dialog
      open={open}
      onClose={stay}
      size="sm"
      title={g.title}
      footer={
        <>
          <Button variant="ghost" onClick={leave} disabled={saving}>
            {g.leave}
          </Button>
          <Button
            variant={hasIssues ? 'primary' : 'secondary'}
            data-dialog-primary={hasIssues ? true : undefined}
            onClick={stay}
            disabled={saving}
          >
            {g.stay}
          </Button>
          {hasIssues ? null : (
            <Button
              data-dialog-primary
              disabled={saving}
              onClick={() => {
                setSaving(true);
                void onSaveAndLeave().then((saved) => {
                  setSaving(false);
                  if (saved) leave();
                  else stay();
                });
              }}
            >
              {g.saveAndLeave}
            </Button>
          )}
        </>
      }
    >
      <p className="m-0 text-md text-fg-secondary">{hasIssues ? g.bodyIssues : g.body}</p>
    </Dialog>
  );
}
