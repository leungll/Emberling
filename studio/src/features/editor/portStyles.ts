import type { PortDataType } from '@/api/types';

/**
 * Port colour encodes the data type only (04 §2.2). Execution status is shown by the node
 * border and status badge and must never reuse these colours.
 */
const PORT_COLORS: Record<PortDataType, string> = {
  text: '#a855f7', // purple
  image: '#ec4899', // pink
  json: '#eab308', // yellow
  any: '#9ca3af', // gray
};

export function portColor(dataType: PortDataType): string {
  return PORT_COLORS[dataType] ?? PORT_COLORS.any;
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
