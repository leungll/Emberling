import type { VariantProps } from 'class-variance-authority';
import type { ButtonHTMLAttributes } from 'react';

import { cn } from '@/lib/utils';

import { buttonVariants } from './button-variants';

export type ButtonProps = ButtonHTMLAttributes<HTMLButtonElement> &
  VariantProps<typeof buttonVariants>;

export function Button({ className, variant, size, ...props }: ButtonProps) {
  return <button className={cn(buttonVariants({ variant, size }), className)} {...props} />;
}
