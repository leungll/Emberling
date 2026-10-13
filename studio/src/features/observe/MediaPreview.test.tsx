import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import { MediaPreview } from './MediaPreview';

describe('MediaPreview', () => {
  it('previews each image a value names, with its kind and reference', () => {
    render(
      <MediaPreview
        runId="run_1"
        value={{
          image: { source: 'EXTERNAL', uri: 'https://images.example/a.png' },
          caption: 'A cat on a sofa',
        }}
      />,
    );
    const img = screen.getByRole('img', { name: 'Preview: Image from an outside service' });
    expect(img).toHaveAttribute('src', 'https://images.example/a.png');
    expect(screen.getByText('https://images.example/a.png')).toBeInTheDocument();
  });

  it('shows only the reference when no route serves it', () => {
    render(
      <MediaPreview
        runId="run_1"
        value={{ image: { source: 'EXTERNAL', uri: 'file:///etc/passwd' } }}
      />,
    );
    expect(screen.queryByRole('img')).toBeNull();
    expect(screen.getByText('No preview for this reference.')).toBeInTheDocument();
  });

  it('renders nothing for a value without images', () => {
    const { container } = render(<MediaPreview runId="run_1" value={{ text: 'hello' }} />);
    expect(container).toBeEmptyDOMElement();
  });
});
