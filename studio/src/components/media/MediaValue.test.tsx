import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import { MediaRow, MediaValue } from './MediaValue';
import { findMedia } from './media-field';
import { MediaSourceContext } from './media-source';

const image = { key: 'cover', title: 'Cover', value: 'asset_cover', mediaType: 'image/png' };
const video = { key: 'clip', title: 'Clip', value: 'asset_clip', mediaType: 'video/mp4' };
const source = {
  runId: 'run_1',
  runInput: {
    photos: [
      { assetId: 'asset_cover', mediaType: 'image/png' },
      { assetId: 'asset_clip', mediaType: 'video/mp4' },
    ],
  },
};

const withSource = (ui: React.ReactElement) => (
  <MediaSourceContext.Provider value={source}>{ui}</MediaSourceContext.Provider>
);

describe('MediaValue', () => {
  it('renders nothing for a value the resolver cannot place', () => {
    const { container } = render(<MediaValue media={image} />);
    expect(container).toBeEmptyDOMElement();
  });

  it('shows an image from the resolver’s address, never a guessed one', () => {
    render(withSource(<MediaValue media={image} />));
    const img = screen.getByRole('img', { name: 'Cover' });
    expect(img).toHaveAttribute('src', expect.stringMatching(/\/assets\/asset_cover\/content$/));
  });

  it('shows a video as a still that plays on click', () => {
    render(withSource(<MediaValue media={video} caption={null} />));
    expect(screen.getByRole('button', { name: 'Play Clip' })).toBeInTheDocument();
    expect(screen.getByText('Clip')).toBeInTheDocument();
  });
});

describe('MediaRow', () => {
  it('renders only its facts when the media does not resolve', () => {
    render(
      <MediaRow media={video}>
        <span>Fact</span>
      </MediaRow>,
    );
    expect(screen.getByText('Fact')).toBeInTheDocument();
    expect(screen.queryByRole('button')).not.toBeInTheDocument();
  });

  it('puts the thumbnail beside the facts when it resolves', () => {
    render(
      withSource(
        <MediaRow media={video}>
          <span>Fact</span>
        </MediaRow>,
      ),
    );
    expect(screen.getByRole('button', { name: 'Play Clip' })).toBeInTheDocument();
    expect(screen.getByText('Fact')).toBeInTheDocument();
  });
});

describe('findMedia', () => {
  it('picks the first schema field with a media type that resolves', () => {
    const schema = {
      properties: {
        clip: { contentMediaType: 'video/mp4', title: 'Clip' },
        note: { title: 'Note' },
      },
    };
    expect(findMedia(schema, { clip: 'asset_clip', note: 'x' }, source)?.title).toBe('Clip');
    expect(findMedia(schema, { clip: 'asset_unknown' }, source)).toBeNull();
    expect(findMedia(null, { clip: 'asset_clip' }, source)).toBeNull();
  });
});
