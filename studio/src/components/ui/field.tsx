import { CircleAlert } from 'lucide-react';
import {
  forwardRef,
  useId,
  type InputHTMLAttributes,
  type ReactNode,
  type TextareaHTMLAttributes,
} from 'react';

import { copy } from '@/lib/copy';
import { cn } from '@/lib/utils';

export const controlClasses =
  'w-full rounded-md border border-border-control bg-surface-inset px-3 text-md text-fg transition-colors duration-1 placeholder:text-fg-placeholder hover:border-fg-muted focus-visible:border-focus focus-visible:outline-offset-0 aria-invalid:border-danger disabled:cursor-not-allowed disabled:opacity-50';

export const TextInput = forwardRef<HTMLInputElement, InputHTMLAttributes<HTMLInputElement>>(
  function TextInput({ className, ...props }, ref) {
    return <input ref={ref} className={cn(controlClasses, 'h-control-md', className)} {...props} />;
  },
);

export const TextArea = forwardRef<
  HTMLTextAreaElement,
  TextareaHTMLAttributes<HTMLTextAreaElement>
>(function TextArea({ className, ...props }, ref) {
  return (
    <textarea
      ref={ref}
      className={cn(controlClasses, 'min-h-control-md py-1.5', className)}
      {...props}
    />
  );
});

interface FieldProps {
  label: string;
  /** Marks the rarer of required or optional: "(optional)" after the label. */
  optional?: boolean;
  /** Marks a required field with an asterisk read as "required". */
  required?: boolean;
  /** Why, what format, or what an empty value does; one sentence under the control. */
  hint?: ReactNode;
  error?: string | null;
  /** Something at the right of the label row, such as a Fields | JSON switch. */
  aside?: ReactNode;
  /** Renders the control with the ids it must carry. */
  children: (control: {
    id: string;
    'aria-describedby': string | undefined;
    'aria-invalid': boolean | undefined;
  }) => ReactNode;
  className?: string;
}

/** A form field: label row, control, hint and error slot. */
export function Field({
  label,
  optional,
  required,
  hint,
  error,
  aside,
  children,
  className,
}: FieldProps) {
  const id = useId();
  const hintId = `${id}-hint`;
  const errorId = `${id}-error`;
  const describedBy = [hint ? hintId : null, error ? errorId : null].filter(Boolean).join(' ');
  return (
    <div className={cn('flex flex-col gap-1', className)}>
      <div className="flex items-baseline gap-2">
        <label htmlFor={id} className="text-sm font-medium text-fg-secondary">
          {label}
          {optional ? (
            <>
              {' '}
              <span className="font-normal text-fg-muted">{copy.form.optional}</span>
            </>
          ) : null}
          {required ? (
            <>
              <span aria-hidden="true" className="text-fg-muted">
                {' *'}
              </span>
              <span className="sr-only"> {copy.form.required}</span>
            </>
          ) : null}
        </label>
        {aside ? <div className="ml-auto">{aside}</div> : null}
      </div>
      {children({
        id,
        'aria-describedby': describedBy || undefined,
        'aria-invalid': error ? true : undefined,
      })}
      {hint ? (
        <p id={hintId} className="m-0 text-sm text-fg-muted">
          {hint}
        </p>
      ) : null}
      {error ? (
        <p id={errorId} className="m-0 flex items-start gap-1.5 text-sm text-danger-fg">
          <CircleAlert className="mt-0.5 size-icon-sm shrink-0" aria-hidden="true" />
          {error}
        </p>
      ) : null}
    </div>
  );
}
