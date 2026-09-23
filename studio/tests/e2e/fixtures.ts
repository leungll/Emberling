// A minimal local copy of `src/api/types.ts`'s `CreateDefinitionRequest`, not an import:
// `tests/` and `src/` are separate TypeScript projects (tsconfig.node.json vs
// tsconfig.app.json), and a project may only reference declarations from another composite
// project's build output, not its plain source files - importing across them fails `tsc
// -b` with TS6307. `studio/src/api/client.ts`'s `createDefinition` is the authority this
// shape must keep matching; it is intentionally not re-exported for cross-project reuse
// (CLAUDE.md: no opportunistic restructuring for one E2E suite).
interface DefinitionNode {
  id: string;
  type: string;
  name: string;
  position: { x: number; y: number };
  config: Record<string, unknown>;
}

interface DefinitionEdge {
  id: string;
  source: string;
  sourceHandle: string;
  target: string;
  targetHandle: string;
}

export interface CreateDefinitionRequest {
  name: string;
  description: string;
  nodes: DefinitionNode[];
  edges: DefinitionEdge[];
}

/**
 * Definition payloads for the three MVP acceptance scenarios (docs/04-ux.md §6, docs/09
 * §1). `global-setup.ts` creates each of these directly through `POST /api/definitions`
 * (never through the UI - only Run creation is driven from Studio), so a spec file only
 * has to open the Run Input Dialog and observe.
 *
 * `documentProcessing` reproduces `backend/test/fixtures/definitions/document_processing.json`
 * verbatim, minus its `workflowId` (the create request never accepts one; the Backend
 * assigns it).
 */
export const documentProcessing: CreateDefinitionRequest = {
  name: 'Document Processing (E2E)',
  description: 'Summarise a document and translate the summary',
  nodes: [
    {
      id: 'node_input',
      type: 'text_input',
      name: 'Source Document',
      position: { x: 0, y: 0 },
      config: {
        inputKey: 'document',
        label: 'Document',
        required: true,
        minLength: 1,
        maxLength: 20000,
      },
    },
    {
      id: 'node_prompt',
      type: 'prompt_template',
      name: 'Summary Prompt',
      position: { x: 200, y: 0 },
      config: { template: 'Summarise the following document:\n{{text}}' },
    },
    {
      id: 'node_summary',
      type: 'text_generation',
      name: 'Summary',
      position: { x: 400, y: 0 },
      config: { modelId: 'text-model-v1' },
    },
    {
      id: 'node_translation',
      type: 'text_generation',
      name: 'Translation',
      position: { x: 600, y: 0 },
      config: { modelId: 'text-model-v1' },
    },
    {
      id: 'node_output',
      type: 'text_output',
      name: 'Result',
      position: { x: 800, y: 0 },
      config: {},
    },
  ],
  edges: [
    {
      id: 'edge_input_prompt',
      source: 'node_input',
      sourceHandle: 'text',
      target: 'node_prompt',
      targetHandle: 'text',
    },
    {
      id: 'edge_prompt_summary',
      source: 'node_prompt',
      sourceHandle: 'text',
      target: 'node_summary',
      targetHandle: 'prompt',
    },
    {
      id: 'edge_summary_translation',
      source: 'node_summary',
      sourceHandle: 'text',
      target: 'node_translation',
      targetHandle: 'prompt',
    },
    {
      id: 'edge_translation_output',
      source: 'node_translation',
      sourceHandle: 'text',
      target: 'node_output',
      targetHandle: 'text',
    },
  ],
};

/**
 * `aigcMedia` is a deliberately custom, E2E-only Definition rather than
 * `backend/test/fixtures/definitions/aigc_media.json`: that fixture's `prompt_template` ->
 * `text_generation` chain runs the value through mockmodel's default `ScenarioFinal` echo
 * (`"echo: " + text`) before it ever reaches Image Generation, which destroys the leading
 * `mock:` prefix `backend/internal/adapters/mocktask/adapter.go`'s directive parser
 * requires. Wiring `text_input` straight to `image_generation.prompt` (and, to satisfy
 * `media_output`'s other required port, to `media_output.caption` too - see
 * `backend/internal/nodes/mediaoutput/node.go`) lets the literal Run input reach the Mock
 * Provider unmodified, so `mock:delay:<ms>` (see aigc-media.spec.ts) produces a real,
 * observable `WAITING_CALLBACK` window before the Mock Provider's own scheduled callback
 * (`backend/internal/mockprovider/server.go`'s `Dispatcher.Schedule`) completes it.
 */
export const aigcMedia: CreateDefinitionRequest = {
  name: 'AIGC Media Generation (E2E)',
  description:
    'A text prompt drives Image Generation directly, so a mock: directive reaches the Mock Provider unmodified',
  nodes: [
    {
      id: 'node_input',
      type: 'text_input',
      name: 'Prompt',
      position: { x: 0, y: 0 },
      config: {
        inputKey: 'prompt',
        label: 'Prompt',
        required: true,
        minLength: 1,
        maxLength: 2000,
      },
    },
    {
      id: 'node_image',
      type: 'image_generation',
      name: 'Image',
      position: { x: 200, y: 0 },
      config: { modelId: 'image-model-v1', width: 512 },
    },
    {
      id: 'node_output',
      type: 'media_output',
      name: 'Result',
      position: { x: 400, y: 0 },
      config: {},
    },
  ],
  edges: [
    {
      id: 'edge_input_image',
      source: 'node_input',
      sourceHandle: 'text',
      target: 'node_image',
      targetHandle: 'prompt',
    },
    {
      id: 'edge_input_caption',
      source: 'node_input',
      sourceHandle: 'text',
      target: 'node_output',
      targetHandle: 'caption',
    },
    {
      id: 'edge_image_output',
      source: 'node_image',
      sourceHandle: 'image',
      target: 'node_output',
      targetHandle: 'image',
    },
  ],
};

/**
 * `agentLookup` reproduces `backend/test/fixtures/definitions/agent_lookup.json` verbatim
 * (minus `workflowId`), retargeted at the asynchronous `remote_lookup` Tool, matching
 * `backend/test/contract/e2e_agent_mock_directive_test.go`'s
 * `createAgentAsyncRunFromDirective` (same fixture, same `lookup` -> `remote_lookup` swap).
 * `agent-async-tool.spec.ts` drives it end-to-end via the M5 slice 5.3b mock directive
 * (`mock:tool-call:remote_lookup:<json-arguments>`), reachable purely over HTTP.
 */
export const agentLookup: CreateDefinitionRequest = {
  name: 'Agent Lookup (E2E)',
  description:
    'An Agent that may call the asynchronous remote_lookup Tool before producing a final text answer',
  nodes: [
    {
      id: 'node_input',
      type: 'text_input',
      name: 'Question',
      position: { x: 0, y: 0 },
      config: { inputKey: 'question', label: 'Question', required: true },
    },
    {
      id: 'node_agent',
      type: 'agent',
      name: 'Answering Agent',
      position: { x: 200, y: 0 },
      config: {
        instructions:
          'Answer the question. Call remote_lookup when you need a fact you do not already know.',
        modelId: 'text-model-v1',
        allowedTools: ['remote_lookup'],
        maxTurns: 4,
        timeoutMs: 120000,
      },
    },
    {
      id: 'node_output',
      type: 'text_output',
      name: 'Answer',
      position: { x: 400, y: 0 },
      config: {},
    },
  ],
  edges: [
    {
      id: 'edge_input_agent',
      source: 'node_input',
      sourceHandle: 'text',
      target: 'node_agent',
      targetHandle: 'input',
    },
    {
      id: 'edge_agent_output',
      source: 'node_agent',
      sourceHandle: 'text',
      target: 'node_output',
      targetHandle: 'text',
    },
  ],
};
