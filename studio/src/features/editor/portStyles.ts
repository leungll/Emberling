import type { NodeMetadata, PortDataType } from '@/api/types';

/**
 * Port colour encodes the data type only. Execution status is shown by the node
 * border and status badge and must never reuse these colours.
 */
const PORT_COLORS: Record<PortDataType, string> = {
  text: '#a48afb', // purple
  image: '#ff78b2', // pink
  json: '#fdb022', // amber
  any: '#93a0b5', // slate
};

export function portColor(dataType: PortDataType): string {
  return PORT_COLORS[dataType] ?? PORT_COLORS.any;
}

/**
 * A node's accent colour for the Palette dot, Canvas eyebrow dot and selected-state border:
 * the colour of its primary port type, first output else first input.
 */
export function nodeAccentColor(metadata: NodeMetadata | undefined): string {
  if (!metadata) return portColor('any');
  // A side with no ports (Text Input has no inputs, Text Output no outputs) arrives as an
  // empty array, never `null`, so indexing either side is safe.
  const port = metadata.outputs[0] ?? metadata.inputs[0];
  return port ? portColor(port.dataType) : portColor('any');
}

/**
 * Required ports are solid, optional ports are hollow. React Flow handles are styled
 * inline because the library applies its own base styles to `.react-flow__handle`.
 */
export function portHandleStyle(dataType: PortDataType, required: boolean): React.CSSProperties {
  const color = portColor(dataType);
  return {
    width: 10,
    height: 10,
    borderRadius: 9999,
    border: `2px solid ${color}`,
    background: required ? color : 'transparent',
  };
}
