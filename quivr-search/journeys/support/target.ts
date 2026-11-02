/**
 * Whether journeys that write (add a source, save an alert, paste a text) may
 * run on `base`: only a demo on this machine, such as the throwaway one of
 * `make demo-journeys`. The live demo must never collect test data, whatever
 * the tag filter says.
 */
export function writable(base: string | undefined): boolean {
  if (!base) return false;
  return ['127.0.0.1', 'localhost', '[::1]'].includes(new URL(base).hostname);
}

export const WRITES_ONLY_LOCALLY = 'writes only on a demo on this machine (make demo-journeys)';

/** A word the demo's search and alerts treat as one term, unique to this run. */
export const runWord = () => `journee${Math.random().toString(36).slice(2, 8).replace(/\d/g, 'x')}`;
