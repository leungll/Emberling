/**
 * Generates the id of a node added from the Palette.
 *
 * The id has to be unique within the Definition, and the API does not prescribe a format, so
 * this only needs to be collision-free: a per-generator counter makes two ids from the
 * same generator distinct even when they are minted in the same instant (a double click,
 * or two adds inside one event loop turn), and a random suffix keeps an id minted in one
 * session from colliding with one saved earlier. `randomSuffix` is injectable so a test
 * can pin it and prove the counter alone keeps ids apart.
 */
export type NodeIdGenerator = (nodeType: string) => string;

export function createNodeIdGenerator(randomSuffix: () => string = randomHex): NodeIdGenerator {
  let counter = 0;
  return (nodeType) => {
    counter += 1;
    return `node_${nodeType}_${counter}_${randomSuffix()}`;
  };
}

/** Eight hex characters, from the Web Crypto API when present (jsdom and every supported browser). */
function randomHex(): string {
  const uuid = globalThis.crypto?.randomUUID?.();
  if (uuid) return uuid.slice(0, 8);
  return Math.floor(Math.random() * 0x1_0000_0000)
    .toString(16)
    .padStart(8, '0');
}

/** The generator EditPage uses; one instance per Studio session. */
export const newNodeId: NodeIdGenerator = createNodeIdGenerator();
