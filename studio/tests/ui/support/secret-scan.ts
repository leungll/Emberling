/**
 * Values that must never appear in a recorded fixture: Provider credentials, authorization
 * headers, callback tokens, signing secrets and internal storage keys. The recorder refuses
 * to write a response that matches, and the fixture self-test re-scans every committed file.
 */
const SECRET_KEYS =
  /^(authorization|api[_-]?key|token|callbacktoken|secret|signingsecret|password|storagekey|internalkey)$/i;
const SECRET_VALUES: [RegExp, string][] = [
  [/\bBearer\s+[A-Za-z0-9._~+/-]{8,}/, 'a bearer credential'],
  [/\bsk-[A-Za-z0-9]{16,}/, 'a provider API key'],
  [/[?&](token|signature|sig)=[^&\s"]{8,}/i, 'a signed callback URL'],
  [/callback-signing-secret|mock-provider-key/i, 'a configured secret'],
  [/(^|[\s"'])\/(var|tmp|private)\/[^\s"']*assets/i, 'an internal storage path'],
];

export function findSecrets(value: unknown, at = '$'): string[] {
  if (value === null || value === undefined) return [];
  if (typeof value === 'string') {
    return SECRET_VALUES.filter(([re]) => re.test(value)).map(([, what]) => `${what} at ${at}`);
  }
  if (Array.isArray(value)) return value.flatMap((item, i) => findSecrets(item, `${at}[${i}]`));
  if (typeof value === 'object') {
    return Object.entries(value).flatMap(([key, item]) => {
      const own =
        SECRET_KEYS.test(key) && typeof item === 'string' && item.length > 0
          ? [`a ${key} field at ${at}.${key}`]
          : [];
      return [...own, ...findSecrets(item, `${at}.${key}`)];
    });
  }
  return [];
}
