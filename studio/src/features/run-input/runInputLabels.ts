import type { Node } from '@/api/types';

/**
 * Display labels for run input fields, read from the bound Definition's nodes: a node's
 * `config.inputKey` names the run input property it declares. The field is labelled by
 * the node's non-empty `config.label` when it has one, otherwise by the step's own name
 * (for example the Media Brief "Photo Set", which has no label setting). The label is
 * display only; the submitted field name stays the input key. Only steps that declare
 * run input carry `inputKey` in the registered config schemas, so node type is not
 * re-checked here.
 */
export function runInputLabels(nodes: readonly Node[] | undefined): Record<string, string> {
  const labels: Record<string, string> = {};
  for (const node of nodes ?? []) {
    const { inputKey, label } = node.config ?? {};
    if (typeof inputKey !== 'string') continue;
    if (typeof label === 'string' && label.trim() !== '') {
      labels[inputKey] = label;
    } else if (node.name.trim() !== '') {
      labels[inputKey] = node.name;
    }
  }
  return labels;
}
