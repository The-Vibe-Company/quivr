// Which articles this browser has read. The engine does not track reading, so
// the state lives in localStorage only: an article is unread when it arrived
// after this browser's first visit (minus an hour, so a first visit is not
// empty) and was not opened here since.
import { useCallback, useMemo, useState } from "react";
import type { FeedItem } from "./feed";

const KEY = "quivr-demo.read.v1";
const MAX = 2000;
const FIRST_VISIT_WINDOW = 3600_000;

interface Stored {
  since: string;
  read: string[];
}

const id = (item: Pick<FeedItem, "record_id" | "version_id">) =>
  `${item.record_id}:${item.version_id}`;

function load(): Stored {
  try {
    const saved = JSON.parse(localStorage.getItem(KEY) || "null");
    if (
      saved &&
      typeof saved.since === "string" &&
      Array.isArray(saved.read)
    )
      return saved;
  } catch {
    // Unreadable storage: start as a first visit.
  }
  const fresh = {
    since: new Date(Date.now() - FIRST_VISIT_WINDOW).toISOString(),
    read: [],
  };
  save(fresh);
  return fresh;
}

function save(state: Stored) {
  try {
    localStorage.setItem(KEY, JSON.stringify(state));
  } catch {
    // Without storage, reading still works for this page's lifetime.
  }
}

export function useReadState() {
  const [state, setState] = useState(load);
  const read = useMemo(() => new Set(state.read), [state.read]);
  const isUnread = useCallback(
    (item: FeedItem) => {
      const at = item.received_at || item.published_at;
      return !!at && at > state.since && !read.has(id(item));
    },
    [state.since, read],
  );
  const markRead = useCallback((item: Pick<FeedItem, "record_id" | "version_id">) => {
    setState((current) => {
      if (current.read.includes(id(item))) return current;
      const next = { ...current, read: [...current.read, id(item)].slice(-MAX) };
      save(next);
      return next;
    });
  }, []);
  return { isUnread, markRead };
}
