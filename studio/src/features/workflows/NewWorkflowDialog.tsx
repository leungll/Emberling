import { useRef, useState, type SyntheticEvent } from 'react';

import { Button } from '@/components/ui/button';
import { Dialog } from '@/components/ui/dialog';
import { Field, TextArea, TextInput } from '@/components/ui/field';
import { useCopy } from '@/lib/copy';

interface NewWorkflowDialogProps {
  open: boolean;
  onClose: () => void;
  /** Called with the trimmed name (never empty) and description. */
  onCreate: (name: string, description: string) => void;
}

/** Fits a textarea to its content, so the description grows as it is typed. */
function fitHeight(el: HTMLTextAreaElement | null): void {
  if (!el) return;
  el.style.height = 'auto';
  el.style.height = `${String(el.scrollHeight + el.offsetHeight - el.clientHeight)}px`;
}

/**
 * Collects the name and description of a new workflow. Nothing is saved here: Create opens
 * an unsaved draft in the Editor, and its first Save creates version 1. Create stays
 * available; a missing name is reported when the form is submitted.
 */
export function NewWorkflowDialog({ open, onClose, onCreate }: NewWorkflowDialogProps) {
  const copy = useCopy();
  const d = copy.workflows.newDialog;
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [invalid, setInvalid] = useState(false);
  const nameRef = useRef<HTMLInputElement>(null);
  const formId = 'new-workflow-form';

  const reset = () => {
    setName('');
    setDescription('');
    setInvalid(false);
  };

  const close = () => {
    reset();
    onClose();
  };

  const submit = () => {
    const trimmed = name.trim();
    if (!trimmed) {
      setInvalid(true);
      nameRef.current?.focus();
      return;
    }
    const desc = description.trim();
    reset();
    onCreate(trimmed, desc);
  };

  const onFormSubmit = (e: SyntheticEvent) => {
    e.preventDefault();
    submit();
  };

  return (
    <Dialog
      open={open}
      onClose={close}
      title={d.title}
      size="sm"
      onSubmit={submit}
      footer={
        <>
          <Button variant="secondary" size="lg" onClick={close}>
            {d.cancel}
          </Button>
          <Button type="submit" form={formId} size="lg" data-dialog-primary>
            {d.create}
          </Button>
        </>
      }
    >
      <form id={formId} noValidate onSubmit={onFormSubmit} className="flex flex-col gap-4">
        <Field label={d.name} error={invalid ? d.nameMissing : null}>
          {(p) => (
            <TextInput
              {...p}
              ref={nameRef}
              value={name}
              autoComplete="off"
              placeholder={d.namePlaceholder}
              onChange={(e) => {
                setName(e.target.value);
                if (invalid && e.target.value.trim()) setInvalid(false);
              }}
              onBlur={() => {
                if (name && !name.trim()) setInvalid(true);
              }}
            />
          )}
        </Field>
        <Field label={d.description} optional>
          {(p) => (
            <TextArea
              {...p}
              rows={2}
              value={description}
              onChange={(e) => {
                setDescription(e.target.value);
                fitHeight(e.target);
              }}
              className="min-h-16 resize-none"
            />
          )}
        </Field>
      </form>
    </Dialog>
  );
}
