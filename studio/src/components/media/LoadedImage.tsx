import { ImageIcon } from 'lucide-react';
import { useState, type CSSProperties, type ReactNode } from 'react';

import { cn } from '@/lib/utils';

/**
 * An image that falls back to the neutral image placeholder when it fails to load or decodes
 * to no size, so a broken address never shows the browser's broken-image icon. The decision
 * comes from the image's own load events, never from its address.
 */
export function LoadedImage({
  src,
  alt,
  className,
  style,
  loading,
  placeholder,
  iconClassName = 'size-icon-md',
}: {
  src: string;
  alt: string;
  className?: string;
  style?: CSSProperties;
  loading?: 'lazy' | 'eager';
  /** A line under the placeholder's icon, such as "Image unavailable". */
  placeholder?: ReactNode;
  iconClassName?: string;
}) {
  const [broken, setBroken] = useState(false);
  if (broken) {
    return (
      <span
        role="img"
        aria-label={alt}
        className="flex size-full flex-col items-center justify-center gap-1 p-2 text-center text-sm text-fg-muted"
      >
        <ImageIcon aria-hidden="true" className={iconClassName} />
        {placeholder}
      </span>
    );
  }
  return (
    <img
      src={src}
      alt={alt}
      loading={loading}
      draggable={false}
      style={style}
      className={cn('block', className)}
      onLoad={(e) => {
        const i = e.currentTarget;
        if (i.naturalWidth === 0 && i.naturalHeight === 0) setBroken(true);
      }}
      onError={() => {
        setBroken(true);
      }}
    />
  );
}
