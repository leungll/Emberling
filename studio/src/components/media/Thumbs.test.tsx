import { fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import { Gallery, ThumbSet } from './Thumbs';
import type { MediaItem } from './Lightbox';

const item = (id: string, src: string | null, title = 'Photo'): MediaItem => ({
  ref: { kind: 'ASSET', id, mediaType: 'image/png', sizeBytes: 2048 },
  src,
  title,
});

describe('ThumbSet', () => {
  it('shows at most the given number, the rest behind +N that opens the lightbox there', () => {
    const items = [1, 2, 3, 4, 5, 6].map((n) =>
      item(`asset_${String(n)}`, `/c/${String(n)}`, `Photo ${String(n)}`),
    );
    render(<ThumbSet items={items} max={4} />);
    expect(screen.getAllByRole('button', { name: /^Open preview/ })).toHaveLength(4);
    fireEvent.click(screen.getByRole('button', { name: 'Show 2 more' }));
    const box = screen.getByRole('dialog');
    expect(box).toHaveTextContent('Photo 5');
    expect(box).toHaveTextContent('5 of 6');
  });

  it('steps through the set with the arrow keys', () => {
    render(<ThumbSet items={[item('a', '/c/a', 'Photo 1'), item('b', '/c/b', 'Photo 2')]} />);
    fireEvent.click(screen.getByRole('button', { name: 'Open preview: Photo 1, PNG, 2 KB' }));
    fireEvent.keyDown(document, { key: 'ArrowRight' });
    expect(screen.getByRole('dialog')).toHaveTextContent('Photo 2');
    expect(screen.getByRole('img', { name: 'Photo 2, image 2 of 2' })).toHaveAttribute(
      'src',
      '/c/b',
    );
  });

  it('loads nothing for an image no route serves, and says so in the lightbox', () => {
    render(<ThumbSet items={[item('x', null, 'Linked image')]} />);
    const thumb = screen.getByRole('button', { name: /^Open preview: Linked image/ });
    expect(thumb.querySelector('img')).toBeNull();
    fireEvent.click(thumb);
    expect(screen.getByRole('dialog')).toHaveTextContent('Preview unavailable');
  });
});

describe('Gallery', () => {
  it('captions each image with its title, format, size and ID', () => {
    render(<Gallery items={[item('asset_1', '/c/1', 'Photo 1')]} />);
    const figure = screen.getByRole('figure');
    expect(figure).toHaveTextContent('Photo 1');
    expect(figure).toHaveTextContent('PNG, 2 KB');
    expect(figure).toHaveTextContent('Asset ID');
  });
});
