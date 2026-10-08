// The corpora the demo reads (THE-1335): the demo corpus, created by the
// demo itself, and the corpora QUIVR_DEMO_CORPORA names, found through the
// engine by name or id. Nothing here outlives a reset of the engine's
// databases: a demo corpus that is gone is forgotten and created again, and
// the names are looked up again on each read of the corpus list.
const NAME = "Espace démo";
const MAX_PAGES = 50;
// How long a check that the demo corpus exists holds: the reads of one page
// load share it.
const CHECK_MS = 2000;

const failure = (status, message) => Object.assign(new Error(message), { status });

export function demoCorpora({ upstream, configured = "", onDropped = () => {} }) {
  const entries = [...new Set(configured.split(",").map((entry) => entry.trim()).filter(Boolean))];
  let demo;
  // The latest check that the demo corpus exists: { id, promise, at once done }.
  let checked;
  // The configured corpora found, and the lookup that refreshes them.
  let found = [];
  let lookup;

  /** Every corpus the key can read whose id or name is an entry, and the entries that match none. */
  async function resolve() {
    const listed = [];
    let cursor = "";
    for (let page = 0; page < MAX_PAGES; page++) {
      const query = new URLSearchParams({ limit: "100" });
      if (cursor) query.set("page_cursor", cursor);
      const response = await upstream(`/v0/corpora?${query}`);
      if (response.status !== 200) throw failure(503, `HTTP ${response.status}`);
      listed.push(...response.data.items);
      cursor = response.data.next_page_cursor;
      if (!cursor) break;
    }
    const matches = (entry) => listed.filter((c) => c.corpus_id === entry || c.name === entry);
    return {
      ids: [...new Set(entries.flatMap((entry) => matches(entry).map((c) => c.corpus_id)))],
      missing: entries.filter((entry) => !matches(entry).length),
    };
  }
  // A failed lookup keeps the corpora found before and is tried again on the next read.
  function refresh() {
    const current = (lookup = resolve().then(
      ({ ids }) => {
        // A newer lookup decides: one that answers late is not applied.
        if (lookup !== current) return ids;
        const dropped = found.filter((id) => !ids.includes(id));
        found = ids;
        if (dropped.length) onDropped(dropped);
        return ids;
      },
      (error) => {
        if (lookup === current) lookup = undefined;
        if (found.length) return found;
        throw error;
      },
    ));
    return current;
  }
  function forget(id) {
    if (demo !== id) return;
    demo = undefined;
    // The configured corpora may be gone too: they are looked up again.
    lookup = undefined;
    onDropped([id]);
  }

  return {
    /** The demo corpus's id once known. */
    demo: () => demo,
    /** The demo corpus's id, creating it (idempotently) when it is not known. */
    async ready() {
      if (demo) return demo;
      const response = await upstream("/v0/corpora", "POST", {
        name: NAME,
        idempotency_key: "quivr-web-demo.v1",
      });
      if (response.status !== 201) throw failure(503, "La démo se prépare. Réessayez dans un instant.");
      demo = response.data.corpus_id;
      checked = { id: demo, promise: Promise.resolve(true), at: Date.now() };
      return demo;
    },
    /** The demo corpus first, then the configured ones; fresh looks the names up again. */
    async readable({ fresh = false } = {}) {
      const id = await this.ready();
      if (!entries.length) return [id];
      const ids = await (fresh || !lookup ? refresh() : lookup);
      return [id, ...ids.filter((other) => other !== id)];
    },
    /**
     * Whether the demo corpus is still there; a corpus the engine no longer
     * has is forgotten. A check in progress, or done in the last CHECK_MS
     * unless fresh, answers for it. An engine that does not answer keeps it.
     */
    confirm({ fresh = false } = {}) {
      const id = demo;
      if (!id) return Promise.resolve(true);
      if (checked?.id === id && (!checked.at || (!fresh && Date.now() - checked.at < CHECK_MS)))
        return checked.promise;
      const check = { id };
      check.promise = upstream(`/v0/corpora/${encodeURIComponent(id)}`)
        .then((response) => response.status !== 404, () => true)
        .then((there) => {
          check.at = Date.now();
          if (!there) forget(id);
          return there;
        });
      checked = check;
      return check.promise;
    },
    /** The startup check: one line naming QUIVR_DEMO_CORPORA when an entry matches no corpus. */
    async check() {
      if (!entries.length) return "";
      try {
        const { ids, missing } = await resolve();
        // The first requests read what this check found.
        if (!lookup) {
          found = ids;
          lookup = Promise.resolve(ids);
        }
        if (!missing.length) return "";
        const named = missing.map((entry) => JSON.stringify(entry)).join(", ");
        return `QUIVR_DEMO_CORPORA: no corpus readable with QUIVR_API_KEY is named or identified ${named}; the demo leaves them out.`;
      } catch (error) {
        return `QUIVR_DEMO_CORPORA could not be checked: the engine did not list its corpora (${error.message}).`;
      }
    },
  };
}
