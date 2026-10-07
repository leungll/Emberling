// Returns the URL a fetch stub was called with, whichever RequestInfo form the caller used.
export function requestUrl(input: RequestInfo | URL): string {
  return input instanceof Request ? input.url : input.toString();
}
