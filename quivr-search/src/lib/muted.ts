// Sources hidden from the feed in this browser. Their articles stay in
// Quivr and in search results; only the feed leaves them out.
const KEY = "quivr-muted-sources";

export function loadMuted(): Set<string> {
  try {
    const value = JSON.parse(localStorage.getItem(KEY) || "[]");
    return new Set(Array.isArray(value) ? value.filter((v) => typeof v === "string") : []);
  } catch {
    return new Set();
  }
}

export function saveMuted(muted: Set<string>) {
  try {
    localStorage.setItem(KEY, JSON.stringify([...muted]));
  } catch {
    // Private windows may refuse storage: the choice holds until reload.
  }
}
