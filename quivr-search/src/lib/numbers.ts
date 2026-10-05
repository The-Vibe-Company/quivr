// Numbers the facade counts over every article (quivr-search/catalog.mjs),
// asked again when what they count changes, every minute, every few seconds
// while the facade is still indexing, and after new articles arrive.
import { useEffect, useRef, useState } from "react";
import { APIError } from "./search";

const DEBOUNCE_MS = 150;
const REFRESH_MS = 60000;
const BUILDING_MS = 3000;

/**
 * The answer for `key` (null asks nothing), the previous one meanwhile:
 * `current` says whether it answers this key.
 * `arrivals` changes when articles arrive: they are counted again at most
 * every `every` milliseconds.
 */
export function useNumbers<T extends { building: boolean }>(
  key: string | null,
  load: (signal: AbortSignal) => Promise<T>,
  onUnauthorized: () => void,
  arrivals?: unknown,
  every = 15000,
) {
  const [data, setData] = useState<{ key: string; value: T } | null>(null);
  const [tick, setTick] = useState(0);
  const loader = useRef(load);
  loader.current = load;
  const unauthorized = useRef(onUnauthorized);
  unauthorized.current = onUnauthorized;
  const fetched = useRef(0);

  useEffect(() => {
    if (key === null) return;
    const controller = new AbortController();
    const timer = setTimeout(() => {
      loader
        .current(controller.signal)
        .then((value) => {
          fetched.current = Date.now();
          setData({ key, value });
        })
        .catch((error) => {
          if (controller.signal.aborted) return;
          if (error instanceof APIError && error.status === 401) unauthorized.current();
          // Otherwise the last numbers stay until the next refresh.
        });
    }, DEBOUNCE_MS);
    return () => {
      clearTimeout(timer);
      controller.abort();
    };
  }, [key, tick]);

  useEffect(() => {
    if (key === null) return;
    const timer = setTimeout(
      () => setTick((n) => n + 1),
      data?.value.building ? BUILDING_MS : REFRESH_MS,
    );
    return () => clearTimeout(timer);
  }, [key, data, tick]);

  useEffect(() => {
    if (arrivals === undefined || !fetched.current) return;
    const wait = Math.max(0, fetched.current + every - Date.now());
    const timer = setTimeout(() => setTick((n) => n + 1), wait);
    return () => clearTimeout(timer);
  }, [arrivals, every]);

  if (key === null || !data) return null;
  return { value: data.value, current: data.key === key };
}
