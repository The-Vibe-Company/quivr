// Same-origin demo entrypoint. Core credentials and corpus scope stay server-side.
import http from "node:http";
import { createHash, createHmac, timingSafeEqual } from "node:crypto";
import { readFile, rename, writeFile } from "node:fs/promises";
import { dirname, extname, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";
import { feedGuard, parseSuggestions } from "./feeds.mjs";
import { createFeed } from "./feed.mjs";
import { alertRoutes } from "./alerts.mjs";
import { createAdmin } from "./admin.mjs";
import { activePlugins } from "./admin-plugins.mjs";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "dist");
const core = process.env.QUIVR_API_URL?.replace(/\/$/, "");
const key = process.env.QUIVR_API_KEY;
const password = process.env.DEMO_PASSWORD;
const host = process.env.HOST || "127.0.0.1";
const port = Number(process.env.PORT || 5183);
const secure = process.env.DEMO_SECURE_COOKIE === "true";
if (!core || !key || (!password && host !== "127.0.0.1"))
  throw new Error(
    "Configure QUIVR_API_URL, QUIVR_API_KEY and DEMO_PASSWORD for public serving",
  );
let corpusID = process.env.QUIVR_DEMO_CORPUS_ID;
// Sources (THE-732): feed discovery under the private-address refusal,
// deployment-provided suggestions, and removed sources hidden from lists.
const feeds = feedGuard({
  privateOrigins: (process.env.DEMO_FEED_PRIVATE_ORIGINS || "").split(","),
});
const suggestions = parseSuggestions(
  process.env.DEMO_FEED_SUGGESTIONS,
  console.warn,
);
// Source logos: the icon of the site behind each RSS source, fetched under
// the same refusal and served from this origin (the page's CSP keeps images
// same-origin). Kept a day once found, an hour when the site has none.
const LOGO_TTL = 86400000;
const LOGO_MISS_TTL = 3600000;
const MAX_LOGOS = 200;
const MAX_LOGO_CACHE_BYTES = 16 << 20;
const logos = new Map();
const cachedBytes = () =>
  [...logos.values()].reduce((sum, entry) => sum + (entry.logo?.bytes.length || 0), 0);
function logoFor(feedURL) {
  const known = logos.get(feedURL);
  if (known && (known.pending || Date.now() < known.until)) return known.pending || known.logo;
  const pending = feeds
    .logo(feedURL)
    .catch(() => null)
    .then((logo) => {
      logos.delete(feedURL);
      logos.set(feedURL, { logo, until: Date.now() + (logo ? LOGO_TTL : LOGO_MISS_TTL) });
      // Oldest first, until both the count and the bytes fit.
      for (const [key, entry] of logos) {
        if (logos.size <= MAX_LOGOS && cachedBytes() <= MAX_LOGO_CACHE_BYTES) break;
        if (key !== feedURL && !entry.pending) logos.delete(key);
      }
      return logo;
    });
  logos.set(feedURL, { pending });
  return pending;
}
const stateFile = process.env.DEMO_STATE_FILE;
let removed = new Set();
// Alerts (THE-734) created by the demo, oldest first, and when each was
// created here: the core does not date Subscriptions.
let alertIDs = [];
// Keyed by ids and namespaces people choose: maps without a prototype, so
// a namespace such as "__proto__" is an ordinary key.
let alertDates = Object.create(null);
// Names people gave their sources, by Source Namespace: the core names a
// source by its namespace only, which Records and alerts already use.
let sourceNames = Object.create(null);
if (stateFile)
  try {
    const saved = JSON.parse(await readFile(stateFile, "utf8"));
    removed = new Set(saved.removed || []);
    alertIDs = Array.isArray(saved.alerts) ? saved.alerts : [];
    if (saved.created && typeof saved.created === "object")
      alertDates = Object.assign(Object.create(null), saved.created);
    if (saved.names && typeof saved.names === "object")
      sourceNames = Object.assign(Object.create(null), saved.names);
  } catch (error) {
    if (error.code !== "ENOENT")
      console.warn("DEMO_STATE_FILE is unreadable; starting empty.");
  }
async function saveState() {
  if (!stateFile) return;
  const data = JSON.stringify({
    removed: [...removed],
    alerts: alertIDs,
    created: alertDates,
    names: sourceNames,
  });
  try {
    await writeFile(stateFile + ".tmp", data);
    await rename(stateFile + ".tmp", stateFile);
  } catch {
    // The removal still holds in memory until the next restart.
    console.warn("DEMO_STATE_FILE could not be written.");
  }
}
let feed;
let admin;
const feedFor = (corpus) =>
  (feed ||= createFeed({ core, key, corpus, upstream }));
// The plugins the engine runs, for the Admin tab's Plugins section (THE-797).
const plugins = activePlugins({ upstream: (...args) => upstream(...args) });
// The Admin tab (THE-796) follows the Fil's change stream of the demo corpus.
const adminFor = (corpus) =>
  (admin ||= createAdmin({
    upstream,
    corpus,
    follow: (watcher) => feedFor(corpus).watch(watcher),
  }));
const alerts = alertRoutes({
  upstream: (...args) => upstream(...args),
  jsonBody: (req) => jsonBody(req),
  fail: (status, message) => fail(status, message),
  destination: process.env.QUIVR_DEMO_DESTINATION_ID,
  evaluator: process.env.QUIVR_DEMO_ALERTS_EVALUATOR,
  owner: "quivr-web-demo",
  // Described alerts (THE-763) need a classifier behind the alerts plugin; the
  // deployment says so here rather than the facade probing the core with a
  // throwaway Subscription.
  described: ["1", "true"].includes(process.env.DEMO_DESCRIBED_ALERTS || ""),
  registry: {
    ids: () => [...alertIDs],
    created: (id) => alertDates[id],
    add: async (ids, at) => {
      alertIDs = [...alertIDs, ...ids.filter((id) => !alertIDs.includes(id))];
      if (at) for (const id of ids) alertDates[id] ||= at;
      await saveState();
    },
    remove: async (ids) => {
      alertIDs = alertIDs.filter((id) => !ids.includes(id));
      for (const id of ids) delete alertDates[id];
      await saveState();
    },
  },
});
const equal = (a, b) =>
  timingSafeEqual(
    createHash("sha256").update(a).digest(),
    createHash("sha256").update(b).digest(),
  );
const sign = (value) =>
  createHmac("sha256", key + (password || ""))
    .update("quivr-demo-session:" + value)
    .digest("hex");
const fail = (status, message) => Object.assign(new Error(message), { status });
async function jsonBody(req) {
  if (!req.headers["content-type"]?.startsWith("application/json"))
    throw fail(415, "Requête JSON attendue.");
  const chunks = [];
  let size = 0;
  for await (const chunk of req) {
    size += chunk.length;
    if (size > 1 << 20) throw fail(413, "Le texte est trop volumineux.");
    chunks.push(chunk);
  }
  try {
    return JSON.parse(Buffer.concat(chunks).toString("utf8"));
  } catch {
    throw fail(400, "Requête invalide.");
  }
}
async function upstream(path, method = "GET", body, timeout = 8000) {
  const response = await fetch(core + path, {
    method,
    headers: {
      Authorization: `Bearer ${key}`,
      "Content-Type": "application/json",
    },
    body: body === undefined ? undefined : JSON.stringify(body),
    signal: AbortSignal.timeout(timeout),
    redirect: "error",
  });
  const chunks = [];
  let size = 0;
  for await (const chunk of response.body) {
    size += chunk.length;
    if (size > 2 << 20)
      throw Object.assign(fail(502, "Réponse du moteur invalide."), {
        oversized: true,
      });
    chunks.push(chunk);
  }
  const data = JSON.parse(Buffer.concat(chunks).toString("utf8"));
  return { status: response.status, data };
}
async function readyCorpus() {
  if (corpusID) return corpusID;
  const response = await upstream("/v0/corpora", "POST", {
    name: "Espace démo",
    idempotency_key: "quivr-web-demo.v1",
  });
  if (response.status !== 201)
    throw fail(503, "La démo se prépare. Réessayez dans un instant.");
  corpusID = response.data.corpus_id;
  return corpusID;
}
function authenticated(req) {
  if (!password) return true;
  const cookie =
    req.headers.cookie
      ?.split("; ")
      .find((value) => value.startsWith("quivr_demo="))
      ?.slice(11) || "";
  const [expires, signature = ""] = cookie.split(".");
  return (
    Number(expires) > Date.now() &&
    Number(expires) < Date.now() + 86401000 &&
    equal(signature, sign(expires))
  );
}
function send(res, status, data) {
  res.writeHead(status, {
    "Content-Type": "application/json; charset=utf-8",
    "Cache-Control": "no-store",
  });
  res.end(JSON.stringify(data));
}
async function authorizeRecord(id) {
  const record = await upstream(`/v0/records/${id}`);
  if (record.status >= 500) return record;
  if (
    record.status !== 200 ||
    record.data.source?.corpus_id !== (await readyCorpus())
  )
    throw fail(404, "Document introuvable.");
  return record;
}
// Connector routes are fenced to the demo corpus: an instance of any other
// corpus is reported missing before a read or a mutation is relayed. Secrets
// in request bodies are relayed once and never logged or kept.
async function connectorRoute(req, path, url, corpus) {
  if (path === "/v0/connector-kinds" && req.method === "GET")
    return upstream(path);
  if (path === "/v0/connectors" && req.method === "GET") {
    const query = new URLSearchParams({ corpus_id: corpus });
    for (const name of ["page_cursor", "limit"]) {
      const value = url.searchParams.get(name);
      if (value) query.set(name, value);
    }
    const page = await upstream(`${path}?${query}`);
    if (page.status === 200 && Array.isArray(page.data.items))
      page.data.items = page.data.items
        .filter((item) => !removed.has(item.connector_id))
        .map(named);
    return page;
  }
  if (path === "/v0/connectors" && req.method === "POST") {
    const body = await jsonBody(req);
    if (body.corpus_id !== corpus) throw fail(403, "Corpus non autorisé.");
    if (body.source_namespace === "web-demo")
      throw fail(422, "Cet espace de noms est réservé aux textes ajoutés.");
    if (body.kind === "rss") await feeds.check(body.config?.url);
    return withName(await upstream(path, "POST", body));
  }
  const match = path.match(
    /^\/v0\/connectors\/([\w-]+)(?:\/(disable|credential|schedule|runs))?$/,
  );
  const method = { disable: "POST", credential: "PUT", schedule: "PUT", runs: "POST" }[
    match?.[2]
  ];
  if (!match || req.method !== (method || "GET")) return undefined;
  const body = method ? await jsonBody(req) : undefined;
  const current = await ownConnector(match[1], corpus);
  if (current.status >= 500) return current;
  return withName(method ? await upstream(path, method, body) : current);
}
// A connector as the browser sees it: with the name given to its source.
const named = (connector) =>
  sourceNames[connector.source_namespace]
    ? { ...connector, display_name: sourceNames[connector.source_namespace] }
    : connector;
const withName = (response) =>
  response.status < 300 && response.data?.source_namespace
    ? { ...response, data: named(response.data) }
    : response;
// Renaming a source names its namespace, every instance included; an empty
// name, or the namespace itself, gives the source its own name back.
async function renameSource(req, corpus) {
  const body = await jsonBody(req);
  if (typeof body.connector_id !== "string" || typeof body.name !== "string")
    throw fail(400, "Requête invalide.");
  const name = body.name.replace(/\s+/g, " ").trim();
  if (name.length > 80) throw fail(422, "Le nom d’une source tient en 80 caractères.");
  const current = await ownConnector(body.connector_id, corpus);
  if (current.status >= 500) return current;
  const namespace = current.data.source_namespace;
  if (!name || name === namespace) delete sourceNames[namespace];
  else sourceNames[namespace] = name;
  await saveState();
  return {
    status: 200,
    data: { source_namespace: namespace, display_name: sourceNames[namespace] || null },
  };
}
// A connector of the demo corpus that was not removed, or a 404.
async function ownConnector(id, corpus) {
  const current = await upstream(`/v0/connectors/${encodeURIComponent(id)}`);
  if (current.status >= 500) return current;
  if (
    current.status !== 200 ||
    current.data.corpus_id !== corpus ||
    removed.has(current.data.connector_id)
  )
    throw fail(404, "Connecteur introuvable.");
  return current;
}
// Removing a source disables every instance of its Source Namespace (a
// resumed source has several) and hides them. The core keeps the Records.
async function removeSource(req, corpus) {
  const body = await jsonBody(req);
  if (typeof body.connector_id !== "string")
    throw fail(400, "Requête invalide.");
  const current = await ownConnector(body.connector_id, corpus);
  if (current.status >= 500) return current;
  const namespace = current.data.source_namespace;
  const siblings = [];
  let cursor = "";
  for (let page = 0; page < 50; page++) {
    const query = new URLSearchParams({ corpus_id: corpus });
    if (cursor) query.set("page_cursor", cursor);
    const list = await upstream(`/v0/connectors?${query}`);
    if (list.status !== 200) return list;
    siblings.push(
      ...list.data.items.filter((c) => c.source_namespace === namespace),
    );
    cursor = list.data.next_page_cursor;
    if (!cursor) break;
  }
  for (const c of siblings.filter((c) => c.enabled)) {
    const done = await upstream(
      `/v0/connectors/${encodeURIComponent(c.connector_id)}/disable`,
      "POST",
      { idempotency_key: `demo-remove:${c.connector_id}` },
    );
    if (done.status !== 200) return done;
  }
  for (const c of siblings) removed.add(c.connector_id);
  removed.add(current.data.connector_id);
  await saveState();
  return { status: 200, data: { removed: siblings.map((c) => c.connector_id) } };
}
const server = http.createServer(async (req, res) => {
  res.setHeader("X-Content-Type-Options", "nosniff");
  res.setHeader("Referrer-Policy", "same-origin");
  res.setHeader(
    "Content-Security-Policy",
    "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'",
  );
  try {
    const url = new URL(req.url, "http://localhost");
    const path = url.pathname;
    if (req.method !== "GET" && req.method !== "HEAD") {
      // Mutations must come from this origin. Browsers send Origin on every
      // non-GET fetch; Sec-Fetch-Site covers the rare client that omits it.
      // Only the login may come from a non-browser client (tests, scripts).
      const origin = req.headers.origin;
      const expected = `${secure ? "https" : "http"}://${req.headers.host}`;
      const sameOrigin = origin
        ? origin === expected
        : req.headers["sec-fetch-site"] === "same-origin" ||
          (path === "/demo/login" && !req.headers["sec-fetch-site"]);
      if (!sameOrigin) throw fail(403, "Origine non autorisée.");
    }
    if (path === "/healthz" && req.method === "GET") {
      res.writeHead(204);
      res.end();
      return;
    }
    if (path === "/demo/login" && req.method === "POST") {
      const body = await jsonBody(req);
      if (
        password &&
        (typeof body.password !== "string" || !equal(body.password, password))
      )
        throw fail(401, "Mot de passe incorrect.");
      const expires = String(Date.now() + 86400000);
      res.setHeader(
        "Set-Cookie",
        `quivr_demo=${expires}.${sign(expires)}; Path=/; HttpOnly; SameSite=Strict; Max-Age=86400${secure ? "; Secure" : ""}`,
      );
      send(res, 200, { authenticated: true });
      return;
    }
    if (
      path === "/demo/session" ||
      path.startsWith("/v0/") ||
      path.startsWith("/demo/feeds/") ||
      path === "/demo/sources/remove" ||
      path === "/demo/sources/rename" ||
      path.startsWith("/demo/sources/logo/") ||
      path === "/demo/feed" ||
      path === "/demo/feed/stream" ||
      path === "/demo/feed/page" ||
      path === "/demo/feed/days" ||
      path === "/demo/alerts" ||
      path.startsWith("/demo/alerts/") ||
      path === "/demo/admin" ||
      path.startsWith("/demo/admin/")
    ) {
      if (!authenticated(req))
        throw fail(401, "Ouvrez la démo pour continuer.");
      const id = await readyCorpus();
      if (path === "/demo/session" && req.method === "GET") {
        send(res, 200, { corpus_id: id, name: "Espace démo" });
        return;
      }
      // The Veille page: a snapshot and a live stream of the demo corpus.
      if (
        (path === "/demo/feed" || path === "/demo/feed/stream") &&
        req.method === "GET"
      ) {
        if (path === "/demo/feed") send(res, 200, await feedFor(id).snapshot());
        else await feedFor(id).subscribe(req, res);
        return;
      }
      // Older days of the feed: one period, newest first, and day counts.
      if (path === "/demo/feed/page" && req.method === "GET") {
        send(res, 200, await feedFor(id).page(url.searchParams));
        return;
      }
      if (path === "/demo/feed/days" && req.method === "GET") {
        send(res, 200, await feedFor(id).days(url.searchParams));
        return;
      }
      const logo = path.match(/^\/demo\/sources\/logo\/([\w-]+)$/);
      if (logo && req.method === "GET") {
        const current = await ownConnector(logo[1], id);
        const feedURL = current.data?.kind === "rss" && current.data.config?.url;
        const image = typeof feedURL === "string" && (await logoFor(feedURL));
        if (!image) throw fail(404, "Logo introuvable.");
        res.writeHead(200, {
          "Content-Type": image.type,
          "Content-Length": image.bytes.length,
          "Cache-Control": "private, max-age=86400",
        });
        res.end(image.bytes);
        return;
      }
      // The Admin tab, read-only: a snapshot, a live stream and one timeline.
      if (path === "/demo/admin" && req.method === "GET") {
        send(res, 200, await adminFor(id).snapshot());
        return;
      }
      if (path === "/demo/admin/stream" && req.method === "GET") {
        await adminFor(id).subscribe(req, res);
        return;
      }
      let response;
      const timeline = path.match(
        /^\/demo\/admin\/documents\/([\w-]+)\/timeline$/,
      );
      const stats = path.match(/^\/demo\/admin\/stats\/([\w-]+)$/);
      if (timeline && req.method === "GET")
        response = await adminFor(id).timeline(timeline[1]);
      else if (stats && req.method === "GET")
        response = await adminFor(id).stats(stats[1], url);
      else if (path === "/demo/admin/plugins" && req.method === "GET")
        response = await plugins();
      else if (path.startsWith("/demo/alerts"))
        response = await alerts(req, path, id);
      else if (path === "/demo/feeds/suggestions" && req.method === "GET")
        response = { status: 200, data: { items: suggestions } };
      else if (path === "/demo/feeds/discover" && req.method === "POST") {
        const body = await jsonBody(req);
        response = { status: 200, data: await feeds.discover(body.url) };
      } else if (path === "/demo/sources/remove" && req.method === "POST")
        response = await removeSource(req, id);
      else if (path === "/demo/sources/rename" && req.method === "POST")
        response = await renameSource(req, id);
      else if (path === "/v0/search" && req.method === "POST") {
        const body = await jsonBody(req);
        if (
          !Array.isArray(body.corpus_ids) ||
          body.corpus_ids.length !== 1 ||
          body.corpus_ids[0] !== id
        )
          throw fail(403, "Corpus non autorisé.");
        // The body goes through unchanged, profile included. A deep search
        // may take up to the engine's hard bound, 9 s, before it answers.
        response = await upstream(path, "POST", body, 10000);
      } else if (path === "/v0/search/profiles" && req.method === "GET") {
        // The profiles the engine answers: the page offers deep only when listed.
        response = await upstream(path);
      } else if (path === "/v0/records" && req.method === "POST") {
        const body = await jsonBody(req);
        if (
          body.source?.corpus_id !== id ||
          body.source?.namespace !== "web-demo"
        )
          throw fail(403, "Corpus non autorisé.");
        response = await upstream(path, "POST", body);
      } else if (path.startsWith("/v0/connector")) {
        response = await connectorRoute(req, path, url, id);
      } else if (path === "/v0/changes" && req.method === "GET") {
        const query = new URLSearchParams({ corpus_id: id, limit: "100" });
        const cursor = url.searchParams.get("cursor");
        if (cursor) query.set("cursor", cursor);
        response = await upstream(`${path}?${query}`);
      } else if (req.method === "GET") {
        const record = path.match(
          /^\/v0\/records\/([\w-]+)(?:\/versions\/([\w-]+))?$/,
        );
        const receipt = path.match(/^\/v0\/ingestion-receipts\/([\w-]+)$/);
        if (record) {
          response = await authorizeRecord(record[1]);
          if (record[2] && response.status === 200)
            response = await upstream(path);
        } else if (receipt) {
          response = await upstream(path);
          if (
            response.status < 500 &&
            (response.status !== 200 || response.data.source?.corpus_id !== id)
          )
            throw fail(404, "Ajout introuvable.");
        }
      }
      if (!response) throw fail(404, "Page introuvable.");
      send(res, response.status, response.data);
      return;
    }
    if (req.method !== "GET" && req.method !== "HEAD")
      throw fail(405, "Méthode non autorisée.");
    const relative =
      decodeURIComponent(path) === "/"
        ? "/index.html"
        : decodeURIComponent(path);
    const file = resolve(root, "." + relative);
    if (!file.startsWith(root + sep)) throw fail(404, "Page introuvable.");
    let data;
    try {
      data = await readFile(file);
    } catch {
      throw fail(404, "Page introuvable.");
    }
    const mime =
      {
        ".html": "text/html; charset=utf-8",
        ".js": "text/javascript",
        ".css": "text/css",
        ".svg": "image/svg+xml",
        ".png": "image/png",
        ".woff2": "font/woff2",
      }[extname(file)] || "application/octet-stream";
    res.writeHead(200, {
      "Content-Type": mime,
      "Cache-Control": relative.startsWith("/assets/")
        ? "public,max-age=31536000,immutable"
        : "no-cache",
    });
    res.end(req.method === "HEAD" ? undefined : data);
  } catch (error) {
    const status = error.status || 503;
    send(res, status, {
      code:
        error.status && error.code
          ? error.code
          : status === 503
            ? "demo_unavailable"
            : "demo_request_failed",
      message: error.status
        ? error.message
        : "Le moteur est momentanément indisponible. Réessayez.",
      retryable: status === 503,
    });
  }
});
server.requestTimeout = 15000;
server.headersTimeout = 10000;
server.listen(port, host, () =>
  console.log(`Quivr demo listening on ${host}:${server.address().port}`),
);
