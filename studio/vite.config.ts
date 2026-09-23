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
