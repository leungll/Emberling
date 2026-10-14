import { CircleAlert } from 'lucide-react';
import {
  forwardRef,
  useId,
  useLayoutEffect,
  useRef,
  type InputHTMLAttributes,
  type ReactNode,
  type RefObject,
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

const MAX_GROW = 14 * 20 + 14;

/** A text area that grows with its text up to about 14 lines, then scrolls. */
export function GrowArea(
  props: React.TextareaHTMLAttributes<HTMLTextAreaElement> & {
    max?: number;
    ref?: RefObject<HTMLTextAreaElement | null>;
  },
) {
  const { max = MAX_GROW, className, ref: outer, ...rest } = props;
  const inner = useRef<HTMLTextAreaElement>(null);
  const ref = outer ?? inner;
  useLayoutEffect(() => {
    const ta = ref.current;
    if (!ta) return;
    ta.style.height = 'auto';
    ta.style.height = `${String(Math.min(ta.scrollHeight + 2, max))}px`;
  }, [props.value, max, ref]);
  return <TextArea ref={ref} {...rest} className={cn('resize-y', className)} />;
}

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
  /**
   * Renders the control with the ids it must carry. A control that is a group rather than
   * one element is named by the label's id, passed apart so it never spreads onto an element.
   */
  children: (
    control: {
      id: string;
      'aria-describedby': string | undefined;
      'aria-invalid': boolean | undefined;
    },
    label: { labelId: string },
  ) => ReactNode;
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
        <label id={`${id}-label`} htmlFor={id} className="text-sm font-medium text-fg-secondary">
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
      {children(
        {
          id,
          'aria-describedby': describedBy || undefined,
          'aria-invalid': error ? true : undefined,
        },
        { labelId: `${id}-label` },
      )}
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
