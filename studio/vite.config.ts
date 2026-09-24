import type { IncomingMessage, ServerResponse } from 'node:http';
import { fileURLToPath, URL } from 'node:url';

import tailwindcss from '@tailwindcss/vite';
import react from '@vitejs/plugin-react';
import { defineConfig } from 'vitest/config';

// The Backend owns every execution fact, so the dev server only forwards /api to it.
// Studio never falls back to a mock or in-browser Runtime when the Backend is absent.
const BACKEND_ORIGIN = process.env.EMBERLING_BACKEND_ORIGIN ?? 'http://localhost:8080';

// Shared by `server` (local dev) and `preview` (deploy/Dockerfile.studio, Playwright's
// webServer) so the two modes proxy identically and never drift.
const API_PROXY = {
  '/api': {
    target: BACKEND_ORIGIN,
    changeOrigin: true,
    // SSE must stream; buffering the proxy response would delay Event delivery.
    ws: false,
    configure: (proxy: {
      on(
        event: 'proxyRes',
        fn: (proxyRes: IncomingMessage, req: unknown, res: ServerResponse) => void,
      ): void;
    }) => {
      // A Backend that dies mid-stream (crash, restart) aborts the upstream response; the
      // proxy only pipes it, so without this the browser's SSE connection would stay open
      // on a dead upstream and Observe would never see the drop that triggers its
      // reconnect-from-last-seq (runObserver.ts). Ending the client response instead makes
      // EventSource reconnect, exactly as it does against the Backend directly.
      proxy.on('proxyRes', (proxyRes, _req, res) => {
        proxyRes.on('close', () => {
          if (!res.writableEnded) res.end();
        });
      });
    },
  },
};

export default defineConfig({
  plugins: [tailwindcss(), react()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    port: 5173,
    proxy: API_PROXY,
  },
  preview: {
    port: 5173,
    proxy: API_PROXY,
  },
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./src/test/setup.ts'],
    css: false,
    include: ['src/**/*.{test,spec}.{ts,tsx}'],
    // Playwright specs drive a real browser and are run by `pnpm e2e`.
    exclude: ['node_modules/**', 'dist/**', 'tests/e2e/**'],
  },
});
