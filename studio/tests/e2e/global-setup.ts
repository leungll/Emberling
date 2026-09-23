import { mkdir, writeFile } from 'node:fs/promises';
import path from 'node:path';

import type { FullConfig } from '@playwright/test';

import {
  agentLookup,
  aigcMedia,
  documentProcessing,
  type CreateDefinitionRequest,
} from './fixtures';

// Only the two fields this file needs from the Backend's Definition response - see
// fixtures.ts's comment on why this is a local copy rather than an import from
// `src/api/types`.
interface CreatedDefinition {
  workflowId: string;
  version: number;
}

const BACKEND_URL = process.env.EMBERLING_E2E_BACKEND_URL ?? 'http://localhost:8080';
const MOCKPROVIDER_URL = process.env.EMBERLING_E2E_MOCKPROVIDER_URL ?? 'http://localhost:9101';

// Playwright's `globalSetup` runs in its own process; its return value is not shared with
// the worker processes that run each spec file (only `globalTeardown` gets it back). The
// three Definitions created here are instead written to this file, which every spec reads.
// `studio/package.json` sets `"type": "module"`, so this file runs as real ESM (no
// `__dirname`); `import.meta.dirname` is Node's ESM-native equivalent (Node 20.11+/21.2+,
// well under this repository's `.nvmrc` baseline).
export const GENERATED_DEFINITIONS_PATH = path.join(
  import.meta.dirname,
  '.generated',
  'definitions.json',
);

export interface GeneratedDefinitions {
  documentProcessing: { workflowId: string; version: number };
  aigcMedia: { workflowId: string; version: number };
  agentLookup: { workflowId: string; version: number };
}

async function waitForReady(url: string, label: string, deadlineMs: number): Promise<void> {
  const start = Date.now();
  let lastError: unknown;
  while (Date.now() - start < deadlineMs) {
    try {
      const response = await fetch(url, { signal: AbortSignal.timeout(2000) });
      if (response.ok) return;
      lastError = new Error(`${label} answered ${response.status}`);
    } catch (error) {
      lastError = error;
    }
    await new Promise((resolve) => setTimeout(resolve, 250));
  }
  throw new Error(`${label} did not become ready within ${deadlineMs}ms: ${String(lastError)}`);
}

// The Backend is the sole authority for Definitions (CLAUDE.md: Studio owns no Runtime
// behaviour), so every fixture is created directly against its REST API - never through
// the Studio UI, which the acceptance scenarios reserve for starting and observing Runs.
async function createDefinition(request: CreateDefinitionRequest): Promise<CreatedDefinition> {
  const response = await fetch(`${BACKEND_URL}/api/definitions`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(request),
  });
  if (!response.ok) {
    const body = await response.text();
    throw new Error(
      `POST /api/definitions failed for "${request.name}": ${response.status} ${body}`,
    );
  }
  return (await response.json()) as CreatedDefinition;
}

export default async function globalSetup(_config: FullConfig): Promise<void> {
  // `/ready` is the full startup gate (migrations applied, Registries populated); `/health`
  // is liveness only and would let a not-yet-migrated Backend pass this wait.
  await waitForReady(`${BACKEND_URL}/ready`, 'backend /ready', 60_000);
  await waitForReady(`${MOCKPROVIDER_URL}/healthz`, 'mockprovider /healthz', 60_000);

  const [documentProcessingDefinition, aigcMediaDefinition, agentLookupDefinition] =
    await Promise.all([
      createDefinition(documentProcessing),
      createDefinition(aigcMedia),
      createDefinition(agentLookup),
    ]);

  const generated: GeneratedDefinitions = {
    documentProcessing: {
      workflowId: documentProcessingDefinition.workflowId,
      version: documentProcessingDefinition.version,
    },
    aigcMedia: { workflowId: aigcMediaDefinition.workflowId, version: aigcMediaDefinition.version },
    agentLookup: {
      workflowId: agentLookupDefinition.workflowId,
      version: agentLookupDefinition.version,
    },
  };

  await mkdir(path.dirname(GENERATED_DEFINITIONS_PATH), { recursive: true });
  await writeFile(GENERATED_DEFINITIONS_PATH, JSON.stringify(generated, null, 2));
}
