// The live data every page of the dashboard shares: the feed (a snapshot and
// the facade's SSE stream), the alerts with what they caught, and the
// sources. Each hook reads through the facade only.
import { useCallback, useEffect, useRef, useState } from "react";
import { APIError } from "./search";
import { FEED_STREAM, fetchFeed, newestFirst, type FeedItem } from "./feed";
import { fetchAlerts, followMonitoring, type AlertList } from "./alerts";
import { fetchConnectors, type Connector } from "./connectors";

type Status = "loading" | "ready" | "error";

const FRESH_MS = 2800;
// Articles arriving together (a source just added brings dozens) are shown
// together: the page renders once per batch, not once per article.
const ARRIVALS_MS = 250;

/**
 * The feed of the demo corpus, newest first. An article that arrives while
 * `hold()` is true (arrivals paused) waits in `pending` until `showPending`.
 */
export function useFeedStream(
  hold: () => boolean,
  onUnauthorized: () => void,
) {
  const [items, setItems] = useState<FeedItem[]>([]);
  const [pending, setPending] = useState<FeedItem[]>([]);
  const [status, setStatus] = useState<Status>("loading");
  const [error, setError] = useState("");
  const [live, setLive] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const [fresh, setFresh] = useState<Set<string>>(new Set());
  const [announcement, setAnnouncement] = useState("");
  const holdRef = useRef(hold);
  holdRef.current = hold;
  const pendingRef = useRef<FeedItem[]>([]);
  const itemsRef = useRef<FeedItem[]>([]);

  const flash = useCallback((ids: string[]) => {
    if (!ids.length) return;
    setFresh((current) => new Set([...current, ...ids]));
    setTimeout(
      () =>
        setFresh((current) => {
          const next = new Set(current);
          ids.forEach((id) => next.delete(id));
          return next;
        }),
      FRESH_MS,
    );
  }, []);

  const commit = useCallback(
    (nextItems: FeedItem[], nextPending: FeedItem[]) => {
      itemsRef.current = nextItems;
      pendingRef.current = nextPending;
      setItems(nextItems);
      setPending(nextPending);
    },
    [],
  );

  useEffect(() => {
    const controller = new AbortController();
    let retry: ReturnType<typeof setTimeout>;
    let loads = 0;
    // The batch of live changes not shown yet: the lists are already in the
    // refs, the render, the highlight and the announcement wait for the batch.
    let batch: ReturnType<typeof setTimeout> | undefined;
    let arrived: string[] = [];
    // An article joined those waiting during the batch.
    let held = false;
    const stage = (nextItems: FeedItem[], nextPending: FeedItem[]) => {
      itemsRef.current = nextItems;
      pendingRef.current = nextPending;
      batch ??= setTimeout(() => {
        batch = undefined;
        commit(itemsRef.current, pendingRef.current);
        // Articles removed before the batch was shown are neither lit nor told.
        const shown = arrived.filter((id) =>
          itemsRef.current.some((i) => i.record_id === id),
        );
        flash(shown);
        const waiting = pendingRef.current.length;
        if (waiting && held)
          setAnnouncement(
            `${waiting} nouvel${waiting > 1 ? "s" : ""} article${waiting > 1 ? "s" : ""} en attente.`,
          );
        else if (shown.length > 1)
          setAnnouncement(`${shown.length} nouveaux articles.`);
        else if (shown.length === 1)
          setAnnouncement(
            `Nouveau : ${itemsRef.current.find((i) => i.record_id === shown[0])?.title}`,
          );
        arrived = [];
        held = false;
      }, ARRIVALS_MS);
    };
    // Live changes seen while a snapshot is in flight, replayed over it so an
    // article that arrives meanwhile is not dropped (null marks a removal).
    let replay: Map<string, FeedItem | null> | null = null;
    const without = (list: FeedItem[], id: string) =>
      list.filter((i) => i.record_id !== id);

    const arrive = (item: FeedItem) => {
      const shown = itemsRef.current;
      const waiting = pendingRef.current;
      if (shown.some((i) => i.record_id === item.record_id)) {
        // A new Version of a shown article updates it in place.
        stage(
          [...without(shown, item.record_id), item].sort(newestFirst),
          waiting,
        );
        return;
      }
      if (waiting.length || holdRef.current()) {
        stage(shown, [item, ...without(waiting, item.record_id)]);
        held = true;
        return;
      }
      stage([item, ...shown].sort(newestFirst), waiting);
      arrived.push(item.record_id);
    };
    const drop = (id: string) =>
      stage(without(itemsRef.current, id), without(pendingRef.current, id));

    const load = () => {
      const ticket = ++loads;
      replay = new Map();
      return fetchFeed(controller.signal)
        .then((data) => {
          if (ticket !== loads) return; // A newer snapshot is on its way.
          const waiting = new Set(pendingRef.current.map((i) => i.record_id));
          let list = data.items
            .filter((i) => !waiting.has(i.record_id))
            .sort(newestFirst);
          for (const [id, next] of replay || []) {
            list = without(list, id);
            if (next && !waiting.has(id)) list = [...list, next].sort(newestFirst);
          }
          replay = null;
          commit(list, pendingRef.current);
          // Liveness comes only from the stream's own ordered status events.
          setStatus("ready");
        })
        .catch((e) => {
          if (controller.signal.aborted || ticket !== loads) return;
          replay = null;
          if (e instanceof APIError && e.status === 401)
            return onUnauthorized();
          setError(e instanceof Error ? e.message : "Le fil est indisponible.");
          setStatus("error");
        });
    };
    // The snapshot shows at once; every (re)connection of the stream rereads
    // it, so nothing that arrived while the stream was down is missed.
    const source = new EventSource(FEED_STREAM);
    void load();
    source.onopen = () => void load();
    source.onerror = () => {
      setLive(false);
      if (source.readyState === EventSource.CLOSED) {
        // The facade refused the stream: the snapshot says why.
        void load();
        retry = setTimeout(() => setAttempt((n) => n + 1), 3000);
      }
    };
    source.addEventListener("status", (event) =>
      setLive(JSON.parse((event as MessageEvent).data).live === true),
    );
    source.addEventListener("item", (event) => {
      const item: FeedItem = JSON.parse((event as MessageEvent).data);
      replay?.set(item.record_id, item);
      arrive(item);
    });
    source.addEventListener("remove", (event) => {
      const id = JSON.parse((event as MessageEvent).data).record_id;
      replay?.set(id, null);
      drop(id);
    });
    source.addEventListener("reset", () => void load());
    return () => {
      source.close();
      controller.abort();
      clearTimeout(retry);
      clearTimeout(batch);
    };
  }, [attempt, onUnauthorized, commit, flash]);

  const showPending = useCallback(() => {
    const waiting = pendingRef.current;
    if (!waiting.length) return;
    const ids = new Set(waiting.map((i) => i.record_id));
    commit(
      [
        ...waiting,
        ...itemsRef.current.filter((i) => !ids.has(i.record_id)),
      ].sort(newestFirst),
      [],
    );
    flash([...ids]);
  }, [commit, flash]);

  const retry = useCallback(() => {
    setStatus("loading");
    setAttempt((n) => n + 1);
  }, []);

  return {
    items,
    pending,
    status,
    error,
    live,
    fresh,
    announcement,
    showPending,
    retry,
  };
}

/** The alerts, what each caught (record id → alert ids), kept current. */
export function useAlertList(onUnauthorized: () => void) {
  const [list, setList] = useState<AlertList | null>(null);
  const reload = useCallback(async (signal?: AbortSignal) => {
    try {
      setList(await fetchAlerts(signal));
    } catch (e) {
      if (e instanceof APIError && e.status === 401) onUnauthorized();
    }
  }, [onUnauthorized]);
  useEffect(() => {
    const controller = new AbortController();
    void reload(controller.signal);
    const stop = followMonitoring({
      onChange: (signal) => reload(signal),
      onUnauthorized,
    });
    return () => {
      controller.abort();
      stop();
    };
  }, [reload, onUnauthorized]);
  return { list, reload };
}

const CONNECTORS_INTERVAL = 30000;

/** The demo's Connector Instances, reread on a slow timer and on demand. */
export function useConnectorList(onUnauthorized: () => void) {
  const [connectors, setConnectors] = useState<Connector[]>([]);
  const [available, setAvailable] = useState(true);
  const reload = useCallback(
    async (signal?: AbortSignal) => {
      try {
        setConnectors(await fetchConnectors(signal));
        setAvailable(true);
      } catch (e) {
        if (e instanceof APIError && e.status === 401) onUnauthorized();
        else if (e instanceof APIError && e.status === 403) setAvailable(false);
      }
    },
    [onUnauthorized],
  );
  useEffect(() => {
    const controller = new AbortController();
    void reload(controller.signal);
    const timer = setInterval(() => {
      if (!document.hidden) void reload(controller.signal);
    }, CONNECTORS_INTERVAL);
    return () => {
      controller.abort();
      clearInterval(timer);
    };
  }, [reload]);
  return { connectors, available, reload };
}
