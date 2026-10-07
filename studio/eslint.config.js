import { readdirSync } from 'node:fs';

import js from '@eslint/js';
import reactHooks from 'eslint-plugin-react-hooks';
import reactRefresh from 'eslint-plugin-react-refresh';
import globals from 'globals';
import tseslint from 'typescript-eslint';

const TEST_FILES = ['src/**/*.test.{ts,tsx}', 'src/test/**', 'tests/**'];

// Escapes a module path for use inside a `no-restricted-imports` regex.
const escapeRegex = (value) => value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');

// Feature modules a feature may import from another feature. Every other cross-feature import is rejected,
// so a shared piece either moves to src/components or src/lib, or is added here deliberately. A feature
// directory missing from this map gets an empty allowlist.
const FEATURE_IMPORT_ALLOWLIST = {
  definitions: [],
  editor: ['run-input/RunInputDialog', 'run-input/runInputLabels'],
  observe: [
    'editor/WorkflowCanvas',
    'editor/RegisteredNode',
    'run-input/RunInputDialog',
    'run-input/runInputLabels',
  ],
  'run-input': [],
};

const FEATURES = readdirSync(new URL('./src/features', import.meta.url), { withFileTypes: true })
  .filter((entry) => entry.isDirectory())
  .map((entry) => entry.name);

const APP_IMPORT = {
  regex: '^@/app(/|$)',
  message: 'src/app composes routes; nothing below it may import from it.',
};

const RELATIVE_FEATURE_OR_APP_IMPORT = {
  regex: '^(\\.\\./)+(features|app)(/|$)',
  message: 'Import shared code through the @/ alias; features and app are not shared layers.',
};

// Each file glob below receives exactly one `no-restricted-imports` configuration: a later flat-config block
// matching the same file would replace, not merge, these options.
const featureImportBlocks = FEATURES.map((feature) => {
  const allowed = [
    escapeRegex(`${feature}/`),
    ...(FEATURE_IMPORT_ALLOWLIST[feature] ?? []).map((m) => `${escapeRegex(m)}$`),
  ];
  const allowedAlternation = allowed.join('|');
  return {
    files: [`src/features/${feature}/**/*.{ts,tsx}`],
    rules: {
      'no-restricted-imports': [
        'error',
        {
          patterns: [
            {
              regex: `^@/features/(?!${allowedAlternation})`,
              message: `Feature "${feature}" may not import this module from another feature. Move shared code to src/components or src/lib, or extend the feature import allowlist in eslint.config.js.`,
            },
            {
              regex: '^\\.\\./',
              message: 'Do not reach out of a feature with a relative import; use the @/ alias.',
            },
            APP_IMPORT,
          ],
        },
      ],
    },
  };
});

export default tseslint.config(
  { ignores: ['dist', 'node_modules', 'playwright-report', 'test-results'] },
  { linterOptions: { reportUnusedDisableDirectives: 'error' } },
  {
    files: ['**/*.{ts,tsx}'],
    extends: [
      js.configs.recommended,
      ...tseslint.configs.strictTypeChecked,
      ...tseslint.configs.stylisticTypeChecked,
    ],
    languageOptions: {
      ecmaVersion: 2023,
      globals: globals.browser,
      parserOptions: {
        projectService: true,
        tsconfigRootDir: import.meta.dirname,
      },
    },
    plugins: {
      'react-hooks': reactHooks,
      'react-refresh': reactRefresh,
    },
    rules: {
      ...reactHooks.configs.recommended.rules,
      'react-refresh/only-export-components': ['error', { allowConstantExport: true }],
      '@typescript-eslint/consistent-type-imports': [
        'error',
        { prefer: 'type-imports', fixStyle: 'inline-type-imports' },
      ],
      '@typescript-eslint/no-unused-vars': [
        'error',
        { argsIgnorePattern: '^_', varsIgnorePattern: '^_' },
      ],
      '@typescript-eslint/restrict-template-expressions': ['error', { allowNumber: true }],
      '@typescript-eslint/no-confusing-void-expression': ['error', { ignoreArrowShorthand: true }],
      '@typescript-eslint/no-misused-promises': [
        'error',
        { checksVoidReturn: { attributes: false } },
      ],
      // Studio deliberately keeps fallbacks for status values the Backend may add before the client knows
      // them, and the rule misreads re-entrant flags such as the coalescing re-sync loop in the run observer.
      '@typescript-eslint/no-unnecessary-condition': 'off',
    },
  },
  {
    files: ['**/*.config.{ts,js}', 'tests/**/*.ts'],
    languageOptions: { globals: globals.node },
  },
  {
    files: TEST_FILES,
    rules: {
      '@typescript-eslint/no-non-null-assertion': 'off',
      '@typescript-eslint/no-empty-function': 'off',
    },
  },
  {
    // Backend I/O goes through the typed client and SSE helpers so transport details stay in one place.
    files: ['src/**/*.{ts,tsx}'],
    ignores: ['src/api/**', ...TEST_FILES],
    rules: {
      'no-restricted-globals': [
        'error',
        { name: 'fetch', message: 'Call the Backend through src/api/client.' },
        { name: 'EventSource', message: 'Subscribe to Run Events through src/api/sse.' },
        { name: 'XMLHttpRequest', message: 'Call the Backend through src/api/client.' },
        { name: 'WebSocket', message: 'Run Events arrive over SSE through src/api/sse.' },
      ],
      'no-restricted-properties': [
        'error',
        {
          object: 'window',
          property: 'fetch',
          message: 'Call the Backend through src/api/client.',
        },
        {
          object: 'globalThis',
          property: 'fetch',
          message: 'Call the Backend through src/api/client.',
        },
        {
          object: 'window',
          property: 'EventSource',
          message: 'Subscribe to Run Events through src/api/sse.',
        },
        {
          object: 'globalThis',
          property: 'EventSource',
          message: 'Subscribe to Run Events through src/api/sse.',
        },
      ],
    },
  },
  {
    files: ['src/stores/**/*.{ts,tsx}'],
    rules: {
      'no-restricted-imports': [
        'error',
        {
          patterns: [
            {
              regex: '(^|/)api/(client|sse)$',
              message:
                'Stores hold client interaction state only; Backend I/O stays in features via src/api.',
            },
            {
              regex: '^@/features(/|$)',
              message:
                'Stores hold client interaction state only; Backend I/O stays in features via src/api.',
            },
            APP_IMPORT,
            RELATIVE_FEATURE_OR_APP_IMPORT,
          ],
        },
      ],
    },
  },
  {
    files: ['src/api/**/*.{ts,tsx}', 'src/lib/**/*.{ts,tsx}', 'src/components/**/*.{ts,tsx}'],
    rules: {
      'no-restricted-imports': [
        'error',
        {
          patterns: [
            {
              regex: '^@/features(/|$)',
              message: 'Shared layers (api, lib, components) may not depend on a feature.',
            },
            APP_IMPORT,
            RELATIVE_FEATURE_OR_APP_IMPORT,
          ],
        },
      ],
    },
  },
  ...featureImportBlocks,
);
