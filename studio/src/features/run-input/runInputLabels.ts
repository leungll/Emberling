import type { Node } from '@/api/types';

/**
 * Display labels for run input fields, read from the bound Definition's Input nodes: an
 * Input node's `config.inputKey` names the run input property it declares and its
 * `config.label` is the author-facing name (for example "Creative Brief"). The label is
 * display only; the submitted field name stays the input key. Any node whose config
 * carries both a string `inputKey` and a non-empty `label` contributes; only Input nodes
 * declare `inputKey` in the registered config schemas, so node type is not re-checked here.
 */
export function runInputLabels(nodes: readonly Node[] | undefined): Record<string, string> {
  const labels: Record<string, string> = {};
  for (const node of nodes ?? []) {
    const { inputKey, label } = node.config ?? {};
    if (typeof inputKey === 'string' && typeof label === 'string' && label.trim() !== '') {
      labels[inputKey] = label;
    }
  }
  return labels;
}
