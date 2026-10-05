// Alerts of the demo (THE-734, THE-763), behind facade-owned /demo/alerts routes.
//
// An alert is a Saved Query holding an alerts expression (plugins/alerts) and
// the Subscription that pins it to the alerts evaluator. The facade forces the
// demo corpus, the evaluator, the deployment's webhook destination and one
// opaque Subscription Owner, so the browser only chooses a name and either a
// keyword query or, when the deployment offers them, a plain-language
// description judged by a classifier (`described`, see DEMO_DESCRIBED_ALERTS).
// Every /demo/alerts/:id route first checks that the Subscription has that
// owner and that its Saved Query targets the demo corpus; anything else is 404.
//
// What an alert caught is read from Matches (GET /v0/matches), which exist
// whatever happens to webhook delivery: the demo needs no webhook receiver.
// The core lists only active Subscriptions, so the facade remembers the ids of
// the alerts it created (paused ones included) in a small registry.
//
// POST /demo/alerts/preview asks the core what an alert being written would
// have caught among the newest articles (POST /v0/subscription-previews),
// with the same forced corpus and evaluator; it saves nothing.

const MATCH_PAGES = 10; // 1,000 Matches per alert at most, counted and shown
const SHOWN = 30; // newest caught articles returned by an alert's page
const PARALLEL = 6;
// Bounds of a described alert's description, as in the plugin's schema.
const DESCRIPTION = { min: 3, max: 1000 };
// Source Namespaces a described alert may be limited to, as in the plugin's schema.
const SOURCES_MAX = 64;
const ACTION = /^\/demo\/alerts\/([\w-]+)(?:\/(pause|resume|edit|delete))?$/;
// A preview judges this many of the newest articles (POST /v0/subscription-previews).
// Each article of a described preview is one paid classifier call: fewer of them.
const PREVIEW = { keywords: 50, described: 20 };
const PREVIEW_SHOWN = 3; // caught articles a preview shows

async function mapLimit(items, limit, fn) {
  const out = new Array(items.length);
  let next = 0;
  await Promise.all(
    Array.from({ length: Math.min(limit, items.length) }, async () => {
      while (next < items.length) {
        const i = next++;
        out[i] = await fn(items[i]);
      }
    }),
  );
  return out;
}

const fold = (text) =>
  text
    .normalize("NFKD")
    .replace(/\p{M}/gu, "")
    .replace(/œ/g, "oe")
    .replace(/æ/g, "ae")
    .toLowerCase();

/**
 * About 240 characters of the article around the first matched term, cut on
 * word boundaries; the start of the text when no term is found (a filter-only
 * alert, or a term found in the title only).
 */
export function excerpt(text, terms) {
  const words = [...text.matchAll(/[\p{L}\p{N}]+/gu)];
  const wanted = terms
    .map((term) => [...fold(term).matchAll(/[\p{L}\p{N}]+/gu)].map((m) => m[0]))
    .filter((w) => w.length);
  let at = -1;
  for (let i = 0; i < words.length && at < 0; i++)
    for (const phrase of wanted)
      if (phrase.every((w, j) => words[i + j] && fold(words[i + j][0]) === w)) {
        at = words[i].index;
        break;
      }
  const start = at < 80 ? 0 : text.lastIndexOf(" ", at - 80) + 1;
  const end = Math.min(text.length, start + 240);
  const cut = end < text.length ? text.lastIndexOf(" ", end) : end;
  const body = text.slice(start, cut > start ? cut : end).trim();
  return (start > 0 ? "… " : "") + body + (end < text.length ? " …" : "");
}

export function alertRoutes({
  upstream,
  jsonBody,
  fail,
  destination,
  evaluator,
  owner,
  registry,
  described = false,
}) {
  const [pluginID, pluginVersion] = (evaluator || "alerts@0.2.0").split("@");
  const pin = {
    plugin_id: pluginID,
    version: pluginVersion,
    configuration: {},
  };
  // Saved Query Versions and Record Versions are immutable: read them once.
  const queries = new Map();
  const versions = new Map();
  const sources = new Map();

  const key = (body) => {
    const value = body?.idempotency_key;
    if (typeof value !== "string" || !/^[\w-]{8,80}$/.test(value))
      throw fail(400, "Requête invalide.");
    return value;
  };
  // The core validates the keyword tree against the plugin's schema; the
  // facade only lets through the two kinds, and described ones when offered.
  const expression = (body) => {
    const value = body?.expression;
    if (value?.kind === "described") {
      if (!described)
        throw fail(
          400,
          "Les alertes décrites ne sont pas activées sur ce déploiement.",
        );
      const text =
        typeof value.description === "string" ? value.description.trim() : "";
      if (text.length < DESCRIPTION.min || text.length > DESCRIPTION.max)
        throw fail(
          400,
          `Décrivez l’alerte en ${DESCRIPTION.min} à ${DESCRIPTION.max} caractères.`,
        );
      const sources = value.sources ?? [];
      if (
        !Array.isArray(sources) ||
        sources.length > SOURCES_MAX ||
        sources.some(
          (s) => typeof s !== "string" || !s.trim() || s.length > 256,
        )
      )
        throw fail(400, "Requête invalide.");
      const watched = [...new Set(sources)];
      return {
        kind: "described",
        description: text,
        ...(watched.length ? { sources: watched } : {}),
      };
    }
    if (
      value?.kind !== "keywords" ||
      typeof value.match !== "object" ||
      value.match === null
    )
      throw fail(400, "Requête invalide.");
    return value;
  };
  // An alert's name, 1 to 120 characters.
  const alertName = (body) => {
    const name = typeof body.name === "string" ? body.name.trim() : "";
    if (!name || name.length > 120)
      throw fail(400, "Donnez un nom à l’alerte.");
    return name;
  };
  // Expressions compared whatever the order of their keys.
  const canonical = (value) =>
    JSON.stringify(value, (_, v) =>
      v && typeof v === "object" && !Array.isArray(v)
        ? Object.fromEntries(
            Object.entries(v).sort(([a], [b]) => (a < b ? -1 : 1)),
          )
        : v,
    );
  const definition = (corpus, match) => ({
    corpus_ids: [corpus],
    expression: match,
    retrieval_profile: "default",
    temporal_policy: "from_activation",
  });
  // A core refusal the browser explains (422 invalid_expression, 409 …).
  const refused = (response) =>
    response.status >= 400 && response.status < 500 && response.status !== 404;

  async function queryVersion(sub) {
    const { saved_query_id: id, saved_query_version_id: version } =
      sub.current_version;
    if (queries.has(version)) return queries.get(version);
    const response = await upstream(
      `/v0/saved-queries/${encodeURIComponent(id)}/versions/${encodeURIComponent(version)}`,
    );
    if (response.status !== 200) return response;
    queries.set(version, response.data);
    return response.data;
  }

  // The Subscription and its pinned query when it is one of the demo's alerts;
  // a core outage is passed on, anything else is 404.
  async function own(id, corpus) {
    const sub = await upstream(`/v0/subscriptions/${encodeURIComponent(id)}`);
    if (sub.status >= 500) return { response: sub };
    if (sub.status !== 200 || sub.data.owner !== owner || sub.data.deleted)
      throw fail(404, "Alerte introuvable.");
    const query = await queryVersion(sub.data);
    if (query.status >= 500) return { response: query };
    const corpora = query.definition?.corpus_ids;
    if (query.status || corpora?.length !== 1 || corpora[0] !== corpus)
      throw fail(404, "Alerte introuvable.");
    return { sub: sub.data, query };
  }

  // Positive Matches, oldest first, one per article (its latest Match).
  async function matches(id) {
    const all = [];
    let cursor = "";
    for (let page = 0; page < MATCH_PAGES; page++) {
      const query = new URLSearchParams({ subscription_id: id, limit: "100" });
      if (cursor) query.set("page_cursor", cursor);
      const response = await upstream(`/v0/matches?${query}`);
      if (response.status !== 200) throw response;
      all.push(...response.data.items);
      cursor = response.data.next_page_cursor;
      if (!cursor) break;
    }
    const latest = new Map();
    for (const match of all) {
      latest.delete(match.record_id);
      latest.set(match.record_id, match);
    }
    return { items: [...latest.values()], capped: Boolean(cursor) };
  }

  const view = (sub, query, caught) => ({
    alert_id: sub.subscription_id,
    name: sub.name,
    enabled: sub.enabled,
    kind: query.definition.expression?.kind,
    expression: query.definition.expression,
    match_count: caught.items.length,
    capped: caught.capped,
    // When the demo created it; absent for alerts it found already there.
    created_at: registry.created?.(sub.subscription_id),
  });

  async function article(match) {
    const { record_id: record, record_version_id: version } = match;
    if (!versions.has(version)) {
      const [read, source] = await Promise.all([
        upstream(
          `/v0/records/${encodeURIComponent(record)}/versions/${encodeURIComponent(version)}`,
        ),
        sources.has(record)
          ? { status: 200, data: { source: sources.get(record) } }
          : upstream(`/v0/records/${encodeURIComponent(record)}`),
      ]);
      if (read.status >= 500 || source.status >= 500) throw read;
      if (source.status === 200) sources.set(record, source.data.source);
      versions.set(version, read.status === 200 ? read.data : null);
    }
    const doc = versions.get(version);
    const parts = doc?.manifest?.parts || [];
    const text = (role) =>
      parts.find((p) => p.role === role && p.content?.kind === "text")?.content
        .text || "";
    const details = match.evidence?.details || {};
    const roles = (keys) =>
      (keys || []).map((key) => parts.find((p) => p.key === key)?.role || key);
    // A described alert's evidence carries the classifier's score instead of terms.
    const score = typeof details.score === "number" ? details.score : null;
    const threshold =
      typeof details.threshold === "number" ? details.threshold : null;
    const terms = Array.isArray(details.terms)
      ? details.terms.map((t) => ({ term: t.term, parts: roles(t.part_keys) }))
      : [];
    const body = text("body");
    // A text added by hand has no title: its opening words stand for one, and
    // the excerpt shows only when the text goes on beyond them.
    const cut = body.lastIndexOf(" ", 90);
    const title =
      text("title") ||
      (body.length > 90 ? body.slice(0, cut > 40 ? cut : 90) + " …" : body);
    return {
      match_id: match.match_id,
      record_id: record,
      version_id: version,
      available: Boolean(doc),
      title,
      excerpt:
        body && body !== title
          ? excerpt(
              body,
              terms.map((t) => t.term),
            )
          : "",
      source: sources.get(record)?.namespace || "",
      explanation: match.evidence?.explanation || "",
      terms,
      fields: Array.isArray(details.fields) ? details.fields : [],
      score,
      threshold,
    };
  }

  async function list(corpus) {
    // Active alerts of the owner, then the registry, which also holds paused ones.
    const active = [];
    let cursor = "";
    for (let page = 0; page < 20; page++) {
      const query = new URLSearchParams({ owner, limit: "100" });
      if (cursor) query.set("page_cursor", cursor);
      const response = await upstream(`/v0/subscriptions?${query}`);
      if (response.status === 403)
        return {
          status: 200,
          data: { available: false, described, items: [], matched: {} },
        };
      if (response.status !== 200) return response;
      active.push(...response.data.items.map((s) => s.subscription_id));
      cursor = response.data.next_page_cursor;
      if (!cursor) break;
    }
    const known = registry.ids();
    const added = active.filter((id) => !known.includes(id));
    if (added.length) await registry.add(added);
    const gone = [];
    const items = await mapLimit(registry.ids(), PARALLEL, async (id) => {
      try {
        const { sub, query, response } = await own(id, corpus);
        if (response) throw response;
        const caught = await matches(id);
        return { alert: view(sub, query, caught), caught };
      } catch (error) {
        if (error.status === 404 && !("data" in error)) gone.push(id);
        else throw error;
      }
    });
    if (gone.length) await registry.remove(gone);
    const found = items.filter(Boolean).reverse();
    // Which alerts caught each article, for the live feed's badge.
    const matched = {};
    for (const { alert, caught } of found)
      for (const m of caught.items)
        (matched[m.record_id] ||= []).push(alert.alert_id);
    return {
      status: 200,
      data: {
        available: true,
        described,
        items: found.map((f) => f.alert),
        matched,
      },
    };
  }

  async function create(req, corpus) {
    const body = await jsonBody(req);
    const idem = key(body);
    const match = expression(body);
    const name = alertName(body);
    const saved = await upstream("/v0/saved-queries", "POST", {
      idempotency_key: `demo-alert:${idem}`,
      name,
      definition: definition(corpus, match),
    });
    if (saved.status !== 201 && saved.status !== 200) return saved;
    const sub = await upstream("/v0/subscriptions", "POST", {
      idempotency_key: `demo-alert-subscription:${idem}`,
      name,
      saved_query_id: saved.data.saved_query_id,
      saved_query_version_id: saved.data.current_version.version_id,
      evaluator: pin,
      destination_id: destination,
      owner,
    });
    if (sub.status !== 201 && sub.status !== 200) {
      // An expression the evaluator refuses leaves no orphan Saved Query.
      if (refused(sub))
        await upstream(
          `/v0/saved-queries/${encodeURIComponent(saved.data.saved_query_id)}/delete`,
          "POST",
          { idempotency_key: `demo-alert-discard:${idem}` },
        );
      return sub;
    }
    await registry.add([sub.data.subscription_id], new Date().toISOString());
    return {
      status: 201,
      data: view(sub.data, saved.data.current_version, {
        items: [],
        capped: false,
      }),
    };
  }

  async function act(req, id, action, corpus) {
    const body = await jsonBody(req);
    const idem = key(body);
    const { sub, query, response } = await own(id, corpus);
    if (response) return response;
    const path = `/v0/subscriptions/${encodeURIComponent(id)}`;
    let result;
    if (action === "pause" || action === "resume")
      result = await upstream(
        `${path}/${action === "pause" ? "disable" : "enable"}`,
        "POST",
        { idempotency_key: `demo-alert-${action}:${idem}` },
      );
    else if (action === "delete") {
      result = await upstream(`${path}/delete`, "POST", {
        idempotency_key: `demo-alert-delete:${idem}`,
      });
      if (result.status !== 200) return result;
      await upstream(
        `/v0/saved-queries/${encodeURIComponent(sub.current_version.saved_query_id)}/delete`,
        "POST",
        { idempotency_key: `demo-alert-delete-query:${idem}` },
      );
      await registry.remove([id]);
      return { status: 200, data: { alert_id: id, deleted: true } };
    } else {
      const next = expression(body);
      const name = body.name === undefined ? sub.name : alertName(body);
      // A rename changes the name of the Subscription and of its Saved Query,
      // not their Versions: what the alert caught stays attached to it.
      if (name !== sub.name) {
        result = await upstream(`${path}/rename`, "POST", {
          idempotency_key: `demo-alert-rename:${idem}`,
          name,
        });
        if (result.status !== 200) return result;
        const renamed = await upstream(
          `/v0/saved-queries/${encodeURIComponent(sub.current_version.saved_query_id)}/rename`,
          "POST",
          { idempotency_key: `demo-alert-rename-query:${idem}`, name },
        );
        if (renamed.status !== 200) return renamed;
      }
      if (canonical(next) === canonical(query.definition.expression))
        result = await upstream(path);
      else result = await newVersion(sub, next, idem, corpus);
    }
    if (result.status !== 200) return result;
    const next = result.data.current_version
      ? await queryVersion(result.data)
      : query;
    return { status: 200, data: view(result.data, next, await matches(id)) };
  }

  // Edit: a new Saved Query Version, pinned by a new Subscription Version. It
  // applies to articles that arrive afterwards; Matches keep their Version.
  async function newVersion(sub, next, idem, corpus) {
    const path = `/v0/subscriptions/${encodeURIComponent(sub.subscription_id)}`;
    const version = await upstream(
      `/v0/saved-queries/${encodeURIComponent(sub.current_version.saved_query_id)}/versions`,
      "POST",
      {
        idempotency_key: `demo-alert-edit:${idem}`,
        definition: definition(corpus, next),
      },
    );
    if (version.status !== 201 && version.status !== 200) return version;
    const result = await upstream(`${path}/versions`, "POST", {
      idempotency_key: `demo-alert-edit-subscription:${idem}`,
      saved_query_version_id: version.data.version_id,
      evaluator: pin,
      destination_id: destination,
    });
    if (result.status !== 201 && result.status !== 200) return result;
    queries.set(version.data.version_id, version.data);
    return upstream(path);
  }

  async function detail(id, corpus) {
    const { sub, query, response } = await own(id, corpus);
    if (response) return response;
    const caught = await matches(id);
    const shown = caught.items.slice(-SHOWN).reverse();
    return {
      status: 200,
      data: {
        ...view(sub, query, caught),
        matches: await mapLimit(shown, PARALLEL, article),
      },
    };
  }

  // What an alert being written would have caught among the newest articles,
  // judged by the core with the plugin itself: nothing is saved or sent.
  async function preview(req, corpus) {
    const body = await jsonBody(req);
    const match = expression(body);
    const response = await upstream("/v0/subscription-previews", "POST", {
      definition: definition(corpus, match),
      evaluator: pin,
      limit: PREVIEW[match.kind],
    });
    if (response.status !== 200) return response;
    const result = response.data;
    const shown = result.matches.slice(0, PREVIEW_SHOWN);
    return {
      status: 200,
      data: {
        evaluated: result.evaluated,
        matched: result.matched,
        complete: result.complete,
        items: await mapLimit(shown, PARALLEL, (m) =>
          article({ ...m, match_id: null }),
        ),
      },
    };
  }

  /** The response for an alerts route, or undefined when the path is not one. */
  return async function route(req, path, corpus) {
    if (path !== "/demo/alerts" && !path.startsWith("/demo/alerts/")) return;
    if (!destination)
      return path === "/demo/alerts" && req.method === "GET"
        ? {
            status: 200,
            data: { available: false, described, items: [], matched: {} },
          }
        : undefined;
    try {
      if (path === "/demo/alerts")
        return req.method === "GET"
          ? await list(corpus)
          : req.method === "POST"
            ? await create(req, corpus)
            : undefined;
      if (path === "/demo/alerts/preview")
        return req.method === "POST" ? await preview(req, corpus) : undefined;
      const match = path.match(ACTION);
      if (!match) return;
      if (!match[2])
        return req.method === "GET"
          ? await detail(match[1], corpus)
          : undefined;
      return req.method === "POST"
        ? await act(req, match[1], match[2], corpus)
        : undefined;
    } catch (error) {
      if (path === "/demo/alerts/preview" && error?.name === "TimeoutError")
        return {
          status: 504,
          data: {
            code: "preview_deadline_exceeded",
            message: "Le test de l’alerte a pris trop de temps. Réessayez dans un instant.",
            retryable: true,
          },
        };
      // A core answer thrown out of a loop is relayed as is.
      if (error && typeof error.status === "number" && "data" in error)
        return error;
      throw error;
    }
  };
}
