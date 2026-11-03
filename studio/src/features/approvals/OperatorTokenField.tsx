import { useState, type SyntheticEvent } from 'react';

import { Button } from '@/components/ui/button';
import { Field, TextInput } from '@/components/ui/field';
import { useCopy } from '@/lib/copy';
import { useOperatorStore } from '@/stores/operator-store';

/**
 * Where the viewer gives the operator credential the approval routes require. The value is
 * a password field, goes only into the in-memory operator store, and is never written to
 * browser storage, the address or the page's text.
 */
export function OperatorTokenField({ onChange }: { onChange?: () => void } = {}) {
  const copy = useCopy();
  const a = copy.approvals;
  const token = useOperatorStore((s) => s.token);
  const setToken = useOperatorStore((s) => s.setToken);
  const clearToken = useOperatorStore((s) => s.clearToken);
  const [draft, setDraft] = useState('');

  if (token) {
    return (
      <div className="flex items-center gap-3 text-md text-fg-secondary">
        <span>{a.tokenInUse}</span>
        <Button
          variant="secondary"
          size="sm"
          onClick={() => {
            clearToken();
            onChange?.();
          }}
        >
          {a.forgetToken}
        </Button>
      </div>
    );
  }

  const submit = (e: SyntheticEvent) => {
    e.preventDefault();
    if (!draft.trim()) return;
    setToken(draft);
    setDraft('');
    onChange?.();
  };

  return (
    <form onSubmit={submit} style={{ maxWidth: 560 }} className="flex flex-col gap-3">
      <Field label={a.tokenLabel} hint={a.tokenHelp}>
        {(control) => (
          <TextInput
            {...control}
            type="password"
            autoComplete="off"
            value={draft}
            placeholder={a.tokenPlaceholder}
            onChange={(e) => {
              setDraft(e.target.value);
            }}
          />
        )}
      </Field>
      <div>
        <Button type="submit" unavailableReason={draft.trim() ? undefined : a.tokenEmpty}>
          {a.useToken}
        </Button>
      </div>
    </form>
  );
}
