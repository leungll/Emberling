import { fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import { LoadedImage } from './LoadedImage';

describe('LoadedImage', () => {
  it('shows the image while it loads', () => {
    const { container } = render(<LoadedImage src="/api/assets/a/content" alt="Photo" />);
    expect(container.querySelector('img')?.getAttribute('src')).toBe('/api/assets/a/content');
  });

  it('falls back to the neutral placeholder when the image fails to load', () => {
    const { container } = render(
      <LoadedImage
        src="http://media.internal/x.png"
        alt="Photo"
        placeholder="Preview unavailable"
      />,
    );
    const img = container.querySelector('img');
    if (!img) throw new Error('no img');
    fireEvent.error(img);
    expect(container.querySelector('img')).toBeNull();
    expect(screen.getByRole('img', { name: 'Photo' })).toBeInTheDocument();
    expect(screen.getByText('Preview unavailable')).toBeInTheDocument();
  });

  it('treats an image that decodes to no size as failed', () => {
    const { container } = render(<LoadedImage src="/api/assets/b/content" alt="Photo" />);
    const img = container.querySelector('img');
    if (!img) throw new Error('no img');
    Object.defineProperty(img, 'naturalWidth', { value: 0 });
    Object.defineProperty(img, 'naturalHeight', { value: 0 });
    fireEvent.load(img);
    expect(container.querySelector('img')).toBeNull();
  });
});
