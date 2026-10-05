// Numbers the facade counts over every article (quivr-search/catalog.mjs),
// asked again when what they count changes, every minute, every few seconds
// while the facade is still indexing, and after new articles arrive.
import { useEffect, useRef, useState } from "react";
import { APIError } from "./search";

const REFRESH_MS = 60000;
const BUILDING_MS = 1000;

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
  const [failures, setFailures] = useState(0);
  // A refresh waits for the answer on its way rather than cancel it.
  const busy = useRef(false);
  const again = useRef(false);
  const loader = useRef(load);
  loader.current = load;
  const unauthorized = useRef(onUnauthorized);
  unauthorized.current = onUnauthorized;
  const fetched = useRef(0);

  useEffect(() => {
    if (key === null) return;
    const controller = new AbortController();
    // Asked once the render settles; a newer filter aborts an older answer.
    const timer = setTimeout(() => {
      busy.current = true;
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
          else setFailures((n) => n + 1);
        })
        .finally(() => {
          if (controller.signal.aborted) return;
          busy.current = false;
          if (again.current) {
            again.current = false;
            setTick((n) => n + 1);
          }
        });
    }, 0);
    return () => {
      clearTimeout(timer);
      controller.abort();
      busy.current = false;
      again.current = false;
    };
  }, [key, tick]);

  useEffect(() => {
    if (key === null) return;
    const timer = setTimeout(
      () => {
        if (!busy.current) setTick((n) => n + 1);
      },
      data?.value.building ? BUILDING_MS : REFRESH_MS,
    );
    return () => clearTimeout(timer);
  }, [key, data, tick, failures]);

  useEffect(() => {
    if (arrivals === undefined || !fetched.current) return;
    const wait = Math.max(0, fetched.current + every - Date.now());
    const timer = setTimeout(() => {
      // An answer on its way may predate the arrival: ask again once it is in.
      if (busy.current) again.current = true;
      else setTick((n) => n + 1);
    }, wait);
    return () => clearTimeout(timer);
  }, [arrivals, every]);

  if (key === null || !data) return null;
  return { value: data.value, current: data.key === key };
}
