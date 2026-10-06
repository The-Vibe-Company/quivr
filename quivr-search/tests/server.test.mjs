import { test } from "node:test";
import assert from "node:assert/strict";
import http from "node:http";
import { spawn } from "node:child_process";
import { once } from "node:events";
import { setTimeout as delay } from "node:timers/promises";
import { existsSync, readFileSync } from "node:fs";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import zlib from "node:zlib";

test("upstream read failures remain retryable; out-of-corpus resources stay hidden", async (t) => {
  const upstream = http.createServer((req, res) => {
    const outside = req.url.includes("outside");
    res.writeHead(outside ? 200 : 503, { "Content-Type": "application/json" });
    res.end(
      JSON.stringify(
        outside
          ? { source: { corpus_id: "private-corpus" } }
          : {
              code: "dependency_unavailable",
              message: "Temporarily unavailable",
              retryable: true,
            },
      ),
    );
  });
  upstream.listen(0, "127.0.0.1");
  await once(upstream, "listening");
  t.after(() => {
    upstream.closeAllConnections();
    upstream.close();
  });
  const base = await startDemo(t, upstream.address().port);
  for (const route of [
    "/v0/records/known",
    "/v0/records/known/versions/version",
    "/v0/ingestion-receipts/known",
  ]) {
    const response = await fetch(base + route);
    assert.equal(response.status, 503, route);
    assert.deepEqual(await response.json(), {
      code: "dependency_unavailable",
      message: "Temporarily unavailable",
      retryable: true,
    });
  }
  for (const route of [
    "/v0/records/outside",
    "/v0/records/outside/versions/version",
    "/v0/ingestion-receipts/outside",
  ]) {
    assert.equal(
      (await fetch(base + route)).status,
      404,
      route,
    );
  }
});

async function startDemo(t, upstreamPort, env = {}) {
  const demo = spawn(process.execPath, ["server.mjs"], {
    env: {
      ...process.env,
      HOST: "127.0.0.1",
      PORT: "0",
      DEMO_PASSWORD: "",
      QUIVR_API_URL: `http://127.0.0.1:${upstreamPort}`,
      QUIVR_API_KEY: "fixture-server-key",
      QUIVR_DEMO_CORPUS_ID: "demo",
      ...env,
    },
    stdio: ["ignore", "pipe", "pipe"],
  });
  t.after(async () => {
    if (demo.exitCode === null) {
      demo.kill();
      await once(demo, "exit");
    }
  });
  const ready = await Promise.race([
    once(demo.stdout, "data"),
    delay(5000, undefined, { ref: false }).then(() => {
      throw new Error("demo startup timeout");
    }),
  ]);
  const port = String(ready[0]).match(/127\.0\.0\.1:(\d+)/)?.[1];
  assert.ok(port);
  return `http://127.0.0.1:${port}`;
}

test("search relays the engine's profile list and the chosen profile", async (t) => {
  const seen = [];
  const profiles = {
    items: [
      { name: "default", provider: { kind: "plugin", plugin_id: "core.retrieve" } },
      { name: "deep", provider: { kind: "plugin", plugin_id: "jev.rerank" } },
    ],
  };
  const upstream = http.createServer(async (req, res) => {
    const chunks = [];
    for await (const chunk of req) chunks.push(chunk);
    seen.push({ method: req.method, url: req.url, body: Buffer.concat(chunks).toString("utf8") });
    res.writeHead(200, { "Content-Type": "application/json" });
    res.end(
      JSON.stringify(
        req.url === "/v0/search/profiles"
          ? profiles
          : { items: [], retrieval_profile: { name: "deep", version: "v" } },
      ),
    );
  });
  upstream.listen(0, "127.0.0.1");
  await once(upstream, "listening");
  t.after(() => {
    upstream.closeAllConnections();
    upstream.close();
  });
  const base = await startDemo(t, upstream.address().port);

  const list = await fetch(base + "/v0/search/profiles");
  assert.equal(list.status, 200);
  assert.deepEqual(await list.json(), profiles);
  const body = { query: "q", mode: "hybrid", profile: "deep", limit: 50, corpus_ids: ["demo"] };
  const search = await fetch(base + "/v0/search", {
    method: "POST",
    headers: { Origin: base, "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  assert.equal(search.status, 200);
  assert.deepEqual(JSON.parse(seen.at(-1).body), body);
});

test("connector routes are fenced to the demo corpus and mutations must be same-origin", async (t) => {
  const seen = [];
  const upstream = http.createServer(async (req, res) => {
    const chunks = [];
    for await (const chunk of req) chunks.push(chunk);
    const body = Buffer.concat(chunks).toString("utf8");
    seen.push({
      method: req.method,
      url: req.url,
      body,
      auth: req.headers.authorization,
    });
    const instance = (id) => ({
      connector_id: id,
      corpus_id: id === "connector_outside" ? "private-corpus" : "demo",
    });
    let data = { ok: true };
    const match = req.url.match(/^\/v0\/connectors\/([\w-]+)/);
    if (match) data = instance(match[1]);
    res.writeHead(200, { "Content-Type": "application/json" });
    res.end(JSON.stringify(data));
  });
  upstream.listen(0, "127.0.0.1");
  await once(upstream, "listening");
  t.after(() => {
    upstream.closeAllConnections();
    upstream.close();
  });
  const base = await startDemo(t, upstream.address().port);
  const origin = { Origin: base, "Content-Type": "application/json" };
  const call = (path, init = {}) => fetch(base + path, init);

  // Reads: the list and the change feed are forced to the demo corpus.
  assert.equal((await call("/v0/connector-kinds")).status, 200);
  await call("/v0/connectors?corpus_id=private-corpus&page_cursor=abc");
  await call("/v0/changes?corpus_id=private-corpus&cursor=c1");
  const list = seen.find((r) => r.url.startsWith("/v0/connectors?"));
  const feed = seen.find((r) => r.url.startsWith("/v0/changes?"));
  assert.equal(
    new URL(list.url, "http://x").searchParams.get("corpus_id"),
    "demo",
  );
  assert.equal(
    new URL(list.url, "http://x").searchParams.get("page_cursor"),
    "abc",
  );
  assert.equal(
    new URL(feed.url, "http://x").searchParams.get("corpus_id"),
    "demo",
  );
  assert.equal(new URL(feed.url, "http://x").searchParams.get("cursor"), "c1");
  assert.equal(list.auth, "Bearer fixture-server-key");

  // Instances of another corpus stay hidden, for reads and every mutation.
  assert.equal((await call("/v0/connectors/connector_outside")).status, 404);
  for (const [method, suffix] of [
    ["POST", "/disable"],
    ["PUT", "/credential"],
    ["PUT", "/schedule"],
    ["POST", "/runs"],
  ]) {
    const before = seen.length;
    const response = await call(`/v0/connectors/connector_outside${suffix}`, {
      method,
      headers: origin,
      body: JSON.stringify({ idempotency_key: "k", secret: { token: "t" } }),
    });
    assert.equal(response.status, 404, suffix);
    assert.ok(
      seen.slice(before).every((r) => r.method === "GET"),
      `${suffix} must not be relayed`,
    );
  }
  const rotated = await call("/v0/connectors/connector_demo/credential", {
    method: "PUT",
    headers: origin,
    body: JSON.stringify({ idempotency_key: "k", secret: { token: "t" } }),
  });
  assert.equal(rotated.status, 200);
  assert.equal(seen.at(-1).method, "PUT");
  assert.equal(seen.at(-1).url, "/v0/connectors/connector_demo/credential");

  // Creation is limited to the demo corpus and a namespace of its own.
  const create = (body, headers = origin) =>
    call("/v0/connectors", {
      method: "POST",
      headers,
      body: JSON.stringify(body),
    });
  assert.equal((await create({ corpus_id: "private-corpus" })).status, 403);
  assert.equal(
    (await create({ corpus_id: "demo", source_namespace: "web-demo" })).status,
    422,
  );
  assert.equal(
    (await create({ corpus_id: "demo", source_namespace: "wire" })).status,
    200,
  );

  // Mutations need this origin: cross-origin, or no Origin and no same-origin fetch metadata, is refused.
  const json = { "Content-Type": "application/json" };
  const before = seen.length;
  assert.equal(
    (
      await create(
        { corpus_id: "demo", source_namespace: "wire" },
        { ...json, Origin: "https://untrusted.example" },
      )
    ).status,
    403,
  );
  assert.equal(
    (await create({ corpus_id: "demo", source_namespace: "wire" }, json))
      .status,
    403,
  );
  assert.equal(
    (
      await call("/v0/connectors/connector_demo/schedule", {
        method: "PUT",
        headers: { ...json, "Sec-Fetch-Site": "cross-site" },
        body: JSON.stringify({ interval_seconds: 60 }),
      })
    ).status,
    403,
  );
  assert.equal(seen.length, before, "refused mutations reach the core");
  assert.equal(
    (
      await create(
        { corpus_id: "demo", source_namespace: "wire" },
        { ...json, "Sec-Fetch-Site": "same-origin" },
      )
    ).status,
    200,
  );
  // Unknown connector sub-routes stay closed.
  assert.equal(
    (await call("/v0/connectors/connector_demo/checkpoint")).status,
    404,
  );
});

test("sources: suggestions, guarded discovery and creation, renaming, removal hides every instance", async (t) => {
  const seen = [];
  const instances = {
    connector_a1: { source_namespace: "Example news", enabled: false },
    connector_a2: { source_namespace: "Example news", enabled: true },
    connector_b: { source_namespace: "Other", enabled: true },
    connector_outside: {
      source_namespace: "Example news",
      enabled: true,
      corpus_id: "private-corpus",
    },
  };
  const view = (id) => ({
    connector_id: id,
    corpus_id: "demo",
    ...instances[id],
  });
  const upstream = http.createServer(async (req, res) => {
    const chunks = [];
    for await (const chunk of req) chunks.push(chunk);
    seen.push({
      method: req.method,
      url: req.url,
      body: Buffer.concat(chunks).toString("utf8"),
    });
    let data = { ok: true };
    const disable = req.url.match(/^\/v0\/connectors\/([\w-]+)\/disable$/);
    const one = req.url.match(/^\/v0\/connectors\/([\w-]+)$/);
    if (req.url.startsWith("/v0/connectors?"))
      data = {
        items: Object.keys(instances)
          .map(view)
          .filter((c) => c.corpus_id === "demo"),
      };
    else if (disable) {
      instances[disable[1]].enabled = false;
      data = view(disable[1]);
    } else if (one) data = view(one[1]);
    res.writeHead(200, { "Content-Type": "application/json" });
    res.end(JSON.stringify(data));
  });
  const site = http.createServer((req, res) => {
    res.writeHead(200, { "Content-Type": "application/rss+xml" });
    res.end(
      '<rss version="2.0"><channel><title>Local feed</title></channel></rss>',
    );
  });
  upstream.listen(0, "127.0.0.1");
  site.listen(0, "127.0.0.1");
  await Promise.all([once(upstream, "listening"), once(site, "listening")]);
  t.after(() => {
    upstream.closeAllConnections();
    upstream.close();
    site.close();
  });
  const feed = `http://127.0.0.1:${site.address().port}/feed.xml`;
  const base = await startDemo(t, upstream.address().port, {
    DEMO_FEED_SUGGESTIONS: JSON.stringify([
      { title: "Example news", url: "https://news.example.org/rss" },
      { title: "Broken" },
    ]),
    DEMO_FEED_PRIVATE_ORIGINS: new URL(feed).origin,
  });
  const origin = { Origin: base, "Content-Type": "application/json" };
  const post = (path, body, headers = origin) =>
    fetch(base + path, {
      method: "POST",
      headers,
      body: JSON.stringify(body),
    });

  const listed = await (await fetch(base + "/demo/feeds/suggestions")).json();
  assert.deepEqual(listed, {
    items: [{ title: "Example news", url: "https://news.example.org/rss" }],
  });

  const found = await post("/demo/feeds/discover", { url: feed });
  assert.equal(found.status, 200);
  assert.deepEqual(await found.json(), {
    feeds: [{ url: feed, title: "Local feed" }],
  });
  const refused = await post("/demo/feeds/discover", {
    url: "http://10.0.0.1/feed",
  });
  assert.equal(refused.status, 422);
  assert.equal((await refused.json()).code, "private_address");
  assert.equal(
    (
      await post(
        "/demo/feeds/discover",
        { url: feed },
        {
          "Content-Type": "application/json",
          Origin: "https://untrusted.example",
        },
      )
    ).status,
    403,
  );

  // An rss instance is only relayed when its URL passes the same guard.
  const before = seen.length;
  const privateCreate = await post("/v0/connectors", {
    corpus_id: "demo",
    source_namespace: "Private",
    kind: "rss",
    config: { url: "http://169.254.169.254/latest" },
  });
  assert.equal(privateCreate.status, 422);
  assert.equal((await privateCreate.json()).code, "private_address");
  assert.equal(seen.length, before, "a private feed must not reach the core");
  assert.equal(
    (
      await post("/v0/connectors", {
        corpus_id: "demo",
        source_namespace: "Local",
        kind: "rss",
        config: { url: feed },
      })
    ).status,
    200,
  );

  // Removal disables the enabled instances of the namespace and hides them all.
  assert.equal(
    (await post("/demo/sources/remove", { connector_id: "connector_outside" }))
      .status,
    404,
  );
  const removed = await post("/demo/sources/remove", {
    connector_id: "connector_a1",
  });
  assert.equal(removed.status, 200);
  assert.deepEqual((await removed.json()).removed.sort(), [
    "connector_a1",
    "connector_a2",
  ]);
  const disables = seen.filter((r) => r.url.endsWith("/disable"));
  assert.deepEqual(
    disables.map((r) => r.url),
    ["/v0/connectors/connector_a2/disable"],
  );
  assert.equal(
    JSON.parse(disables[0].body).idempotency_key,
    "demo-remove:connector_a2",
  );
  const after = await (await fetch(base + "/v0/connectors")).json();
  assert.deepEqual(
    after.items.map((c) => c.connector_id),
    ["connector_b"],
  );
  assert.equal((await fetch(base + "/v0/connectors/connector_a2")).status, 404);
  assert.equal((await fetch(base + "/v0/connectors/connector_b")).status, 200);

  // A source's name is the facade's: shown with its instances, and an empty
  // name gives the source its namespace back. The core is not asked.
  const writes = seen.length;
  assert.equal(
    (await post("/demo/sources/rename", { connector_id: "connector_outside", name: "X" })).status,
    404,
  );
  const renamed = await post("/demo/sources/rename", { connector_id: "connector_b", name: "  Le   fil " });
  assert.deepEqual(await renamed.json(), { source_namespace: "Other", display_name: "Le fil" });
  assert.equal((await (await fetch(base + "/v0/connectors")).json()).items[0].display_name, "Le fil");
  assert.equal((await (await fetch(base + "/v0/connectors/connector_b")).json()).display_name, "Le fil");
  await post("/demo/sources/rename", { connector_id: "connector_b", name: "" });
  assert.equal((await (await fetch(base + "/v0/connectors")).json()).items[0].display_name, undefined);
  assert.equal(
    seen.slice(writes).filter((r) => r.method === "POST").length,
    0,
    "renaming never writes to the core",
  );
});

test("source logos: an RSS source of the demo corpus only, raster images served from this origin", async (t) => {
  const png = Buffer.from("89504e470d0a1a0a0000000d494844520000004000000040", "hex");
  const hits = [];
  const site = http.createServer((req, res) => {
    hits.push(req.url);
    const host = `http://${req.headers.host}`;
    const send = (type, body) => {
      res.writeHead(200, { "Content-Type": type });
      res.end(body);
    };
    if (req.url === "/feed") send("application/rss+xml", `<rss><channel><link>${host}/home</link><item/></channel></rss>`);
    else if (req.url === "/home") send("text/html", '<link rel="apple-touch-icon" href="/icon.png">');
    else if (req.url === "/icon.png") send("image/png", png);
    else if (req.url === "/bare") send("application/rss+xml", "<rss><channel><item/></channel></rss>");
    else {
      res.writeHead(404);
      res.end();
    }
  });
  site.listen(0, "127.0.0.1");
  await once(site, "listening");
  const origin = `http://127.0.0.1:${site.address().port}`;
  const connectors = {
    rss_ok: { kind: "rss", config: { url: `${origin}/feed` } },
    rss_bare: { kind: "rss", config: { url: `${origin}/bare` } },
    rss_outside: { kind: "rss", config: { url: `${origin}/feed` }, corpus_id: "private-corpus" },
    other_kind: { kind: "webhook", config: { url: `${origin}/feed` } },
  };
  const upstream = http.createServer((req, res) => {
    const id = req.url.match(/^\/v0\/connectors\/([\w-]+)$/)?.[1];
    const found = id && connectors[id];
    res.writeHead(found ? 200 : 404, { "Content-Type": "application/json" });
    res.end(
      JSON.stringify(
        found
          ? { connector_id: id, corpus_id: "demo", source_namespace: "Example news", ...found }
          : { code: "not_found", message: "Not found" },
      ),
    );
  });
  upstream.listen(0, "127.0.0.1");
  await once(upstream, "listening");
  t.after(() => {
    upstream.closeAllConnections();
    upstream.close();
    site.close();
  });
  const base = await startDemo(t, upstream.address().port, {
    DEMO_FEED_PRIVATE_ORIGINS: origin,
  });
  const logo = await fetch(`${base}/demo/sources/logo/rss_ok`);
  assert.equal(logo.status, 200);
  assert.equal(logo.headers.get("content-type"), "image/png");
  assert.equal(logo.headers.get("x-content-type-options"), "nosniff");
  assert.match(logo.headers.get("content-security-policy"), /default-src 'none'.*sandbox.*frame-ancestors 'none'/);
  assert.match(logo.headers.get("cache-control"), /^private/);
  assert.deepEqual(Buffer.from(await logo.arrayBuffer()), png);
  // A second request is served from the cache, without fetching the site again.
  const fetched = hits.length;
  assert.equal((await fetch(`${base}/demo/sources/logo/rss_ok`)).status, 200);
  assert.equal(hits.length, fetched);
  // Another corpus, another kind, an unknown connector and a site without an
  // icon all answer 404.
  for (const id of ["rss_outside", "other_kind", "missing", "rss_bare"])
    assert.equal((await fetch(`${base}/demo/sources/logo/${id}`)).status, 404, id);
  // A site without an icon is not asked again by the browser for an hour.
  const bare = await fetch(`${base}/demo/sources/logo/rss_bare`);
  assert.equal(bare.headers.get("cache-control"), "private, max-age=3600");
});

test("the Veille feed scans the catalog, relays live Records newest first and never exposes the key", async (t) => {
  const records = {
    rec_rss: { namespace: "wire", version: "v_rss" },
    rec_hand: { namespace: "web-demo", version: "v_hand" },
    rec_gone: { namespace: "wire", version: "v_gone", withdrawn: true },
    rec_other: { namespace: "wire", version: "v_other", corpus: "private" },
  };
  const text = (key, role, value, extra = {}) => ({
    key,
    role,
    content: { kind: "text", text: value },
    ...extra,
  });
  const versions = {
    v_rss: {
      accepted_at: "2026-09-28T07:00:00Z",
      manifest: {
        parts: [
          text("title", "title", "Feed headline"),
          text("body", "body", "Body   of the\narticle"),
          text("body_html", "source_html", "<p>Body</p>", {
            parent_key: "body",
          }),
        ],
      },
      extensions: {
        "connector.rss": {
          schema_version: "1",
          data: {
            item: {
              published: "2026-09-01T08:00:00Z",
              link: "https://news.example.org/headline",
            },
          },
        },
      },
    },
    // A corrected article whose feed gives a link that is not a web address.
    v_rss2: {
      accepted_at: "2026-09-29T10:02:00Z",
      manifest: {
        parts: [
          text("title", "title", "Feed headline, corrected"),
          text("body", "body", "Corrected body"),
        ],
      },
      extensions: {
        "connector.rss": {
          schema_version: "1",
          data: { item: { link: "javascript:alert(1)" } },
        },
      },
    },
    v_hand: {
      accepted_at: "2026-09-28T06:00:00Z",
      manifest: { parts: [text("text", "body", "Pasted note\nSecond line")] },
    },
    v_new: {
      accepted_at: "2026-09-29T09:59:59Z",
      manifest: { parts: [text("text", "body", "Fresh arrival\nIts excerpt")] },
    },
  };
  const seen = [];
  let stream;
  const streamOpened = Promise.withResolvers();
  const upstream = http.createServer((req, res) => {
    seen.push({ url: req.url, auth: req.headers.authorization });
    const url = new URL(req.url, "http://x");
    const json = (status, data) => {
      res.writeHead(status, { "Content-Type": "application/json" });
      res.end(JSON.stringify(data));
    };
    if (url.pathname === "/v0/changes/stream") {
      assert.equal(url.searchParams.get("cursor"), "c0");
      assert.equal(url.searchParams.get("corpus_id"), "demo");
      res.writeHead(200, { "Content-Type": "text/event-stream" });
      res.write(": resumed\n\n");
      stream = res;
      streamOpened.resolve();
      return;
    }
    if (url.pathname === "/v0/changes")
      return json(200, { items: [], next_cursor: "c0", has_more: false });
    if (url.pathname === "/v0/records") {
      assert.equal(url.searchParams.get("corpus_id"), "demo");
      // Newest first, so a restart keeps the latest articles.
      assert.equal(url.searchParams.get("order"), "accepted_at_desc");
      return json(200, {
        items: Object.keys(records).map((record_id) => ({ record_id })),
      });
    }
    const match = url.pathname.match(
      /^\/v0\/records\/(\w+)(?:\/versions\/(\w+))?$/,
    );
    const record = match && records[match[1]];
    if (!record) return json(404, { code: "not_found" });
    if (match[2])
      return json(200, {
        record_id: match[1],
        version_id: match[2],
        ...versions[match[2]],
      });
    json(200, {
      record_id: match[1],
      source: {
        corpus_id: record.corpus || "demo",
        namespace: record.namespace,
        record_key: match[1],
      },
      withdrawn: !!record.withdrawn,
      current_version_id: record.version,
    });
  });
  upstream.listen(0, "127.0.0.1");
  await once(upstream, "listening");
  t.after(() => {
    upstream.closeAllConnections();
    upstream.close();
  });
  const base = await startDemo(t, upstream.address().port);

  const snapshot = await (await fetch(base + "/demo/feed")).json();
  assert.deepEqual(
    snapshot.items.map((item) => item.record_id),
    ["rec_rss", "rec_hand"],
    "withdrawn and out-of-corpus Records stay out; newest arrival first",
  );
  // A catalog scan, as after a restart, dates items by their Version's
  // acceptance, not by the source's publication date.
  assert.deepEqual(snapshot.items[0], {
    record_id: "rec_rss",
    version_id: "v_rss",
    corpus_id: "demo",
    namespace: "wire",
    title: "Feed headline",
    excerpt: "Body of the article",
    received_at: "2026-09-28T07:00:00.000Z",
    published_at: "2026-09-01T08:00:00.000Z",
    link: "https://news.example.org/headline",
  });
  assert.equal(snapshot.items[1].title, "Pasted note");
  assert.equal(snapshot.items[1].excerpt, "Second line");
  assert.ok(seen.every((r) => r.auth === "Bearer fixture-server-key"));

  // Live: a Record event reaches subscribers as a hydrated item.
  const controller = new AbortController();
  t.after(() => controller.abort());
  const live = await fetch(base + "/demo/feed/stream", {
    signal: controller.signal,
  });
  assert.equal(live.status, 200);
  assert.match(live.headers.get("content-type"), /^text\/event-stream/);
  const reader = live.body.getReader();
  const decoder = new TextDecoder();
  let received = "";
  const until = async (pattern) => {
    while (!pattern.test(received)) {
      const { value, done } = await reader.read();
      assert.ok(!done, "stream closed");
      received += decoder.decode(value, { stream: true });
    }
  };
  await streamOpened.promise;
  records.rec_new = { namespace: "web-demo", version: "v_new" };
  const change = (id, cursor) =>
    stream.write(
      `id: ${cursor}\nevent: change\ndata: ${JSON.stringify({
        event_id: cursor,
        type: "record.materialized",
        schema_version: "1",
        occurred_at: "2026-09-29T10:00:00Z",
        resource: { kind: "record", id, corpus_id: "demo" },
        cursor,
      })}\n\n`,
    );
  change("rec_new", "c1");
  await until(/event: item\ndata: [^\n]*"rec_new"/);
  const after = await (await fetch(base + "/demo/feed")).json();
  assert.equal(after.items[0].record_id, "rec_new");
  assert.equal(
    after.items[0].received_at,
    "2026-09-29T09:59:59.000Z",
    "the Version's acceptance, not the event's occurred_at",
  );
  assert.equal(after.items[0].title, "Fresh arrival");
  assert.equal(after.items[0].excerpt, "Its excerpt");
  assert.equal(after.items[0].updated_at, undefined, "a first Version");

  // A new Version with the same title and text (a feed re-dating its items)
  // is not a correction.
  versions.v_new2 = { ...versions.v_new, accepted_at: "2026-09-29T10:05:00Z" };
  records.rec_new.version = "v_new2";
  change("rec_new", "c1a");
  await until(/event: item\ndata: [^\n]*"v_new2"/);
  const redated = (await (await fetch(base + "/demo/feed")).json()).items[0];
  assert.equal(redated.version_id, "v_new2");
  assert.equal(redated.updated_at, undefined, "same text, no correction");

  // A new Version of a known article whose text changed is a correction,
  // dated by that Version's acceptance; a link that is not a web address is
  // dropped.
  records.rec_rss.version = "v_rss2";
  change("rec_rss", "c1b");
  await until(/event: item\ndata: [^\n]*"v_rss2"/);
  const corrected = (await (await fetch(base + "/demo/feed")).json()).items.find(
    (item) => item.record_id === "rec_rss",
  );
  assert.equal(corrected.title, "Feed headline, corrected");
  assert.equal(corrected.updated_at, "2026-09-29T10:02:00.000Z");
  assert.equal(corrected.previous_version_id, "v_rss", "the text it replaces");
  assert.equal(corrected.link, undefined);
  // Re-dating the corrected text keeps pointing at what the correction replaced.
  versions.v_rss3 = versions.v_rss2;
  records.rec_rss.version = "v_rss3";
  change("rec_rss", "c1c");
  await until(/event: item\ndata: [^\n]*"v_rss3"/);
  const kept = (await (await fetch(base + "/demo/feed")).json()).items.find(
    (item) => item.record_id === "rec_rss",
  );
  assert.equal(kept.previous_version_id, "v_rss");

  // A withdrawal removes the item for every reader.
  records.rec_hand.withdrawn = true;
  change("rec_hand", "c2");
  await until(/event: remove\ndata: \{"record_id":"rec_hand"\}/);
  const final = await (await fetch(base + "/demo/feed")).json();
  assert.deepEqual(
    final.items.map((item) => item.record_id),
    ["rec_new", "rec_rss"],
  );
  assert.ok(!received.includes("fixture-server-key"));
  assert.ok(!JSON.stringify(final).includes("fixture-server-key"));

  // An expired cursor mid-stream resynchronizes from the catalog, then
  // tells readers to reread the snapshot.
  const scans = seen.filter((r) => r.url.startsWith("/v0/records?")).length;
  stream.write(
    `event: stream_error\ndata: ${JSON.stringify({ code: "cursor_expired", message: "expired", retryable: false })}\n\n`,
  );
  stream.end();
  await until(/event: reset\n/);
  assert.equal(
    seen.filter((r) => r.url.startsWith("/v0/records?")).length,
    scans + 1,
  );
});

test("an older day of the feed is listed by date and day counts are cached", async (t) => {
  const seen = [];
  const record = (id, extra = {}) => ({
    record_id: id,
    source: { corpus_id: "demo", namespace: "wire", record_key: id },
    withdrawn: false,
    current_version_id: `v_${id}`,
    ...extra,
  });
  const upstream = http.createServer((req, res) => {
    const url = new URL(req.url, "http://x");
    seen.push(url);
    const json = (status, data) => {
      res.writeHead(status, { "Content-Type": "application/json" });
      res.end(JSON.stringify(data));
    };
    if (url.pathname === "/v0/records/count") {
      const after = url.searchParams.get("accepted_after");
      const before = url.searchParams.get("accepted_before");
      if (!after && !before) return json(200, { count: 1234 });
      if (!after) return json(200, { count: before.startsWith("2026-10-02") ? 7 : 0 });
      return json(200, { count: after.startsWith("2026-10-03") ? 3 : 5 });
    }
    if (url.pathname === "/v0/records") {
      if (url.searchParams.get("page_cursor") === "stale")
        return json(409, { code: "cursor_scope_changed" });
      if (url.searchParams.get("page_cursor") === "broken")
        return json(200, { items: [record("broken")] });
      return json(200, {
        items: [
          record("old1"),
          record("gone", { withdrawn: true }),
          record("pending", { current_version_id: undefined }),
          record("old2"),
        ],
        next_page_cursor: "p2",
      });
    }
    const version = url.pathname.match(/^\/v0\/records\/(\w+)\/versions\/(\w+)$/);
    if (version?.[1] === "broken") return json(500, { code: "internal" });
    if (version)
      return json(200, {
        record_id: version[1],
        version_id: version[2],
        accepted_at: "2026-10-02T09:00:00Z",
        manifest: {
          parts: [{ key: "title", role: "title", content: { kind: "text", text: `Title ${version[1]}` } }],
        },
      });
    json(404, { code: "not_found" });
  });
  upstream.listen(0, "127.0.0.1");
  await once(upstream, "listening");
  t.after(() => {
    upstream.closeAllConnections();
    upstream.close();
  });
  const base = await startDemo(t, upstream.address().port);

  // A day in the browser's time zone, newest first, from the page before.
  const after = "2026-10-02T00:00:00+02:00";
  const before = "2026-10-03T00:00:00+02:00";
  const day = new URLSearchParams({ after, before, cursor: "p1" });
  const page = await (await fetch(`${base}/demo/feed/page?${day}`)).json();
  const listed = seen.find((u) => u.pathname === "/v0/records");
  assert.deepEqual(Object.fromEntries(listed.searchParams), {
    corpus_id: "demo",
    order: "accepted_at_desc",
    accepted_after: after,
    accepted_before: before,
    limit: "40",
    page_cursor: "p1",
  });
  assert.deepEqual(
    page.items.map((item) => [item.record_id, item.title, item.received_at]),
    [
      ["old1", "Title old1", "2026-10-02T09:00:00.000Z"],
      ["old2", "Title old2", "2026-10-02T09:00:00.000Z"],
    ],
    "withdrawn Records and Records without a Version stay out, in listing order",
  );
  assert.equal(page.next_cursor, "p2");
  const stale = await fetch(`${base}/demo/feed/page?${new URLSearchParams({ after, before, cursor: "stale" })}`);
  assert.equal(stale.status, 409);
  // A Version that cannot be read now fails the page, to be retried, rather
  // than leave its Record out of a day the cursor has moved past.
  const broken = await fetch(`${base}/demo/feed/page?${new URLSearchParams({ after, before, cursor: "broken" })}`);
  assert.equal(broken.status, 503);
  for (const bad of [
    { after: "2026-10-02", before },
    { after: before, before: after },
    { after: "2026-10-02T00:00:00", before },
  ]) {
    const refused = await fetch(`${base}/demo/feed/page?${new URLSearchParams(bad)}`);
    assert.equal(refused.status, 422, JSON.stringify(bad));
  }

  // Counts: the total, one per day between the bounds, and what is older.
  const bounds = ["2026-10-04T00:00:00+02:00", "2026-10-03T00:00:00+02:00", "2026-10-02T00:00:00+02:00"].join(",");
  const counted = await (await fetch(`${base}/demo/feed/days?${new URLSearchParams({ bounds })}`)).json();
  assert.deepEqual({ ...counted, as_of: undefined }, { total: 1234, days: [3, 5], older: 7, as_of: undefined });
  assert.ok(!Number.isNaN(Date.parse(counted.as_of)));
  const calls = seen.filter((u) => u.pathname === "/v0/records/count").length;
  assert.equal(calls, 4);
  const again = await (await fetch(`${base}/demo/feed/days?${new URLSearchParams({ bounds })}`)).json();
  assert.deepEqual(again, counted, "a second reader within a minute shares the counts");
  assert.equal(seen.filter((u) => u.pathname === "/v0/records/count").length, calls);
  const reversed = await fetch(`${base}/demo/feed/days?${new URLSearchParams({ bounds: bounds.split(",").reverse().join(",") })}`);
  assert.equal(reversed.status, 422);
});

test("the Veille feed and Admin routes need the demo session", async (t) => {
  const seen = [];
  const upstream = http.createServer((req, res) => {
    seen.push(req.url);
    res.writeHead(500);
    res.end();
  });
  upstream.listen(0, "127.0.0.1");
  await once(upstream, "listening");
  t.after(() => {
    upstream.closeAllConnections();
    upstream.close();
  });
  const base = await startDemo(t, upstream.address().port, {
    DEMO_PASSWORD: "fixture-demo-password",
  });
  for (const route of [
    "/demo/feed",
    "/demo/feed/stream",
    "/demo/feed/page",
    "/demo/feed/days",
    "/demo/admin",
    "/demo/admin/stream",
    "/demo/admin/documents/v/timeline",
    "/demo/admin/stats/steps",
    "/demo/admin/history",
    "/demo/sources/logo/connector_any",
  ])
    assert.equal((await fetch(base + route)).status, 401, route);
  assert.deepEqual(seen, [], "nothing reaches the core without a session");
});

test("the Veille feed and the Admin tab explain a key without access", async (t) => {
  const upstream = http.createServer((req, res) => {
    res.writeHead(403, { "Content-Type": "application/json" });
    res.end(
      JSON.stringify({
        code: "forbidden",
        message: "forbidden",
        retryable: false,
      }),
    );
  });
  upstream.listen(0, "127.0.0.1");
  await once(upstream, "listening");
  t.after(() => {
    upstream.closeAllConnections();
    upstream.close();
  });
  const base = await startDemo(t, upstream.address().port);
  const response = await fetch(base + "/demo/feed");
  assert.equal(response.status, 403);
  assert.match((await response.json()).message, /changements/);
  // The stream is refused too, so the browser retries instead of waiting.
  const stream = await fetch(base + "/demo/feed/stream", { signal: AbortSignal.timeout(3000) });
  assert.equal(stream.status, 403);
  await stream.body?.cancel();
  // The Admin tab says which right its key lacks.
  const admin = await fetch(base + "/demo/admin");
  assert.equal(admin.status, 403);
  assert.match((await admin.json()).message, /observability:read/);
});

test("the Admin tab relays the demo corpus's documents live, fences timelines and stays read-only", async (t) => {
  const now = Date.now();
  const at = (ms) => new Date(now - ms).toISOString();
  const docs = [
    {
      version_id: "v_ready", record_id: "r1", corpus_id: "demo", source_namespace: "wire", record_key: "k1",
      title: "Ready", state: "retrieval_ready", is_current: true,
      steps: { accepted_at: at(9000), materialized_at: at(8900), segmented_at: at(8800), retrieval_ready_at: at(8700) },
    },
    {
      version_id: "v_other", record_id: "r2", corpus_id: "private", source_namespace: "wire", record_key: "k2",
      state: "received", is_current: true, steps: { accepted_at: at(5000) },
    },
  ];
  const seen = [];
  let stream;
  const streamOpened = Promise.withResolvers();
  const upstream = http.createServer((req, res) => {
    seen.push({ method: req.method, url: req.url, auth: req.headers.authorization });
    const url = new URL(req.url, "http://x");
    const json = (status, data) => {
      res.writeHead(status, { "Content-Type": "application/json" });
      res.end(JSON.stringify(data));
    };
    if (url.pathname === "/v0/changes/stream") {
      res.writeHead(200, { "Content-Type": "text/event-stream" });
      res.write(": resumed\n\n");
      stream = res;
      streamOpened.resolve();
      return;
    }
    if (url.pathname === "/v0/changes")
      return json(200, { items: [], next_cursor: "c0", has_more: false });
    if (url.pathname === "/v0/records") return json(200, { items: [] });
    // The scan's second page is refused: the first one is kept.
    if (url.pathname === "/v0/admin/documents")
      return url.searchParams.get("page_cursor")
        ? json(422, { code: "invalid_cursor" })
        : json(200, { items: docs, next_page_cursor: "p2" });
    if (url.pathname === "/v0/admin/stats/steps")
      return json(200, {
        window: url.searchParams.get("window"), resolution_seconds: 900, from: at(3_600_000), to: at(0),
        items: [{ step: "accepted_to_searchable", summary: { count: 120, errors: 0, p95_ms: 1500 }, points: [] }],
      });
    const timeline = url.pathname.match(/^\/v0\/admin\/documents\/(\w+)\/timeline$/);
    if (timeline) {
      const doc = docs.find((d) => d.version_id === timeline[1]);
      return doc
        ? json(200, { document: doc, steps: [{ step: "accepted", at: doc.steps.accepted_at }] })
        : json(404, { code: "not_found" });
    }
    if (url.pathname === "/v0/admin/stats/plugins")
      return json(200, {
        window: url.searchParams.get("window"), resolution_seconds: 900, from: at(0), to: at(0), items: [],
      });
    json(404, { code: "not_found" });
  });
  upstream.listen(0, "127.0.0.1");
  await once(upstream, "listening");
  t.after(() => {
    upstream.closeAllConnections();
    upstream.close();
  });
  const base = await startDemo(t, upstream.address().port);

  const snapshot = await (await fetch(base + "/demo/admin")).json();
  assert.deepEqual(snapshot.documents.map((d) => d.version_id), ["v_ready"], "only the demo corpus");
  assert.deepEqual(
    snapshot.documents[0].flow.map((c) => c.state),
    ["done", "done", "done", "run", "run"],
  );
  assert.equal(snapshot.stats.hours_of, "searchable", "the engine's step rollups count");
  assert.equal(snapshot.stats.per_minute, 2);
  assert.equal(snapshot.stats.searchable_p95_ms, 1500);

  // Live: a change in the demo corpus pushes the new document to the stream.
  const controller = new AbortController();
  t.after(() => controller.abort());
  const live = await fetch(base + "/demo/admin/stream", { signal: controller.signal });
  assert.equal(live.status, 200);
  const reader = live.body.getReader();
  const decoder = new TextDecoder();
  let received = "";
  const until = async (pattern) => {
    while (!pattern.test(received)) {
      const { value, done } = await reader.read();
      assert.ok(!done, "stream closed");
      received += decoder.decode(value, { stream: true });
    }
  };
  await streamOpened.promise;
  docs.unshift({
    version_id: "v_new", record_id: "r3", corpus_id: "demo", source_namespace: "web-demo", record_key: "k3",
    state: "received", is_current: true, steps: { accepted_at: at(0) },
  });
  stream.write(
    `id: c1\nevent: change\ndata: ${JSON.stringify({
      event_id: "c1", type: "record.accepted", schema_version: "1", occurred_at: at(0),
      resource: { kind: "record", id: "r3" }, cursor: "c1",
    })}\n\n`,
  );
  await until(/event: documents\ndata: [^\n]*"v_new"/);
  // The next step of that document is sent alone: only what changed.
  received = "";
  docs[0].steps.materialized_at = at(0);
  stream.write(
    `id: c2\nevent: change\ndata: ${JSON.stringify({
      event_id: "c2", type: "record.materialized", schema_version: "1", occurred_at: at(0),
      resource: { kind: "record", id: "r3" }, cursor: "c2",
    })}\n\n`,
  );
  await until(/event: documents\ndata: [^\n]*"materialized_at"[^\n]*\n/);
  assert.doesNotMatch(received, /"v_ready"/);

  // Timelines and rollups: fenced, relayed, never written.
  assert.equal((await fetch(base + "/demo/admin/documents/v_ready/timeline")).status, 200);
  for (const id of ["v_other", "v_unknown"])
    assert.equal((await fetch(base + `/demo/admin/documents/${id}/timeline`)).status, 404, id);
  const plugins = await fetch(base + "/demo/admin/stats/plugins?window=24h");
  assert.equal((await plugins.json()).window, "24h");
  assert.equal((await fetch(base + "/demo/admin/stats/plugins?window=1y")).status, 422);
  assert.equal((await fetch(base + "/demo/admin/stats/anything")).status, 404);
  const post = await fetch(base + "/demo/admin/stats/plugins", {
    method: "POST",
    headers: { Origin: base, "Content-Type": "application/json" },
    body: "{}",
  });
  assert.equal(post.status, 404);
  assert.ok(seen.every((r) => r.method === "GET"), "the Admin tab never writes to the core");
  assert.ok(seen.every((r) => r.auth === "Bearer fixture-server-key"));
  assert.ok(!received.includes("fixture-server-key"));
  assert.ok(!JSON.stringify(snapshot).includes("fixture-server-key"));
});

// A raw GET, so the test sees the bytes and headers on the wire.
const raw = (url, headers = {}) =>
  new Promise((resolve, reject) =>
    http
      .get(url, { headers }, (res) => {
        const chunks = [];
        res.on("data", (chunk) => chunks.push(chunk));
        res.on("end", () =>
          resolve({ status: res.statusCode, headers: res.headers, body: Buffer.concat(chunks) }),
        );
      })
      .on("error", reject),
  );
const decode = ({ headers, body }) =>
  headers["content-encoding"] === "br"
    ? zlib.brotliDecompressSync(body)
    : headers["content-encoding"] === "gzip"
      ? zlib.gunzipSync(body)
      : body;

test("answers are compressed, revalidated with an ETag and say what they waited on in the core", async (t) => {
  const items = Array.from({ length: 50 }, (_, i) => ({
    connector_id: `c${i}`,
    corpus_id: "demo",
    kind: "rss",
    source_namespace: `Source ${i}`,
    enabled: true,
  }));
  const upstream = http.createServer((req, res) => {
    res.writeHead(200, { "Content-Type": "application/json" });
    res.end(JSON.stringify({ items }));
  });
  upstream.listen(0, "127.0.0.1");
  await once(upstream, "listening");
  t.after(() => {
    upstream.closeAllConnections();
    upstream.close();
  });
  const base = await startDemo(t, upstream.address().port);
  for (const coding of ["br", "gzip"]) {
    const first = await raw(`${base}/v0/connectors`, { "Accept-Encoding": coding });
    assert.equal(first.status, 200);
    assert.equal(first.headers["content-encoding"], coding);
    assert.equal(first.headers.vary, "Accept-Encoding");
    assert.deepEqual(JSON.parse(decode(first)).items, items);
    assert.equal(first.headers["cache-control"], "private, no-cache");
    assert.match(first.headers["server-timing"], /^core;dur=[\d.]+;desc="1 call"$/);
    // The same answer read again is a 304 without a body.
    const again = await raw(`${base}/v0/connectors`, {
      "Accept-Encoding": coding,
      "If-None-Match": first.headers.etag,
    });
    assert.equal(again.status, 304);
    assert.equal(again.body.length, 0);
  }
  // A coding the client refuses (q=0) is never chosen.
  const refused = await raw(`${base}/v0/connectors`, { "Accept-Encoding": "br;q=0, gzip" });
  assert.equal(refused.headers["content-encoding"], "gzip");
  // Without Accept-Encoding the answer is plain; errors are never stored.
  const plain = await raw(`${base}/v0/connectors`);
  assert.equal(plain.headers["content-encoding"], undefined);
  assert.deepEqual(JSON.parse(plain.body).items, items);
  const missing = await raw(`${base}/demo/unknown`);
  assert.equal(missing.headers["cache-control"], "no-store");

  // The built bundle, when present (npm run build runs before npm test).
  const index = new URL("../dist/index.html", import.meta.url);
  if (!existsSync(index)) return t.diagnostic("dist/ missing: run npm run build to check the bundle");
  const script = readFileSync(index, "utf8").match(/src="(\/assets\/[^"]+\.js)"/)[1];
  const asset = await raw(base + script, { "Accept-Encoding": "br, gzip" });
  assert.equal(asset.headers["content-encoding"], "br");
  assert.equal(asset.headers["cache-control"], "public,max-age=31536000,immutable");
  assert.deepEqual(decode(asset), readFileSync(new URL("../dist" + script, import.meta.url)));
  assert.ok(asset.body.length < decode(asset).length / 3);
});

test("the demo's numbers count every article of the corpus, beyond the feed's latest 300", async (t) => {
  const { fakeCore } = await import("../scripts/fake-core.mjs");
  const now = Date.now();
  const HOUR = 3600000;
  const DAY = 24 * HOUR;
  // The feed's own scan reads the newest 1,000 articles and keeps 300; only
  // the index reads the 120 older ones, their hours and their titles.
  const core = fakeCore({
    records: [
      ...Array.from({ length: 1000 }, (_, i) => ({
        id: `new${i}`,
        namespace: "Dépêches exemple",
        at: now - (i + 1) * 60000,
        title: `Marché de Noël : étape ${i}`,
      })),
      ...Array.from({ length: 120 }, (_, i) => ({
        id: `old${i}`,
        namespace: "Revue technique",
        at: now - 3 * DAY - (i + 1) * 60000,
        title: `Archives municipales : lot ${i}`,
      })),
    ],
    alerts: [
      ["sub_arch", "Archives", ["archives"]],
      ["sub_new", "Volcans", ["volcan"]],
    ],
  });
  core.server.listen(0, "127.0.0.1");
  await once(core.server, "listening");
  t.after(() => {
    core.server.closeAllConnections();
    core.server.close();
  });
  const iso = (time) => new Date(time).toISOString();
  // The demo noted when it created sub_new; sub_arch was there before it.
  const dir = await mkdtemp(join(tmpdir(), "quivr-demo-"));
  t.after(() => rm(dir, { recursive: true, force: true }));
  const state = join(dir, "state.json");
  await writeFile(state, JSON.stringify({ created: { sub_new: iso(now - DAY) } }));
  const base = await startDemo(t, core.server.address().port, {
    QUIVR_DEMO_DESTINATION_ID: "demo-alerts-sink",
    DEMO_STATE_FILE: state,
  });
  // Today and the three days before, newest first.
  const bounds = [iso(now + HOUR), iso(now - DAY), iso(now - 4 * DAY)];
  const stats = async (body) => {
    const response = await fetch(`${base}/demo/feed/stats`, {
      method: "POST",
      headers: { "Content-Type": "application/json", Origin: base },
      body: JSON.stringify(body),
    });
    return { status: response.status, data: await response.json() };
  };
  const get = async (path) => (await fetch(base + path)).json();
  // Waits until the facade's answer holds, up to a deadline.
  async function until(read, holds, what) {
    const deadline = Date.now() + 5000;
    for (;;) {
      const value = await read();
      if (holds(value)) return value;
      if (Date.now() > deadline) assert.fail(`${what}: ${JSON.stringify(value)}`);
      await delay(25);
    }
  }

  assert.equal((await get("/demo/feed")).items.length, 300);
  const all = await until(() => stats({ buckets: bounds }).then((r) => r.data), (d) => !d.building, "index built");
  assert.deepEqual(
    { total: all.total, buckets: all.buckets, sources: all.sources, alerts: all.alerts, any: all.any_alert },
    {
      total: 1120,
      buckets: [1000, 120],
      sources: { "Dépêches exemple": 1000, "Revue technique": 120 },
      alerts: { sub_arch: 120 },
      any: 120,
    },
  );
  // A picked source narrows the total and the bars, not its own menu.
  const picked = (await stats({ buckets: bounds, sources: ["Revue technique"] })).data;
  assert.deepEqual([picked.total, picked.buckets, picked.sources], [120, [0, 120], all.sources]);
  // Counting again reads memory only: no call reaches the core.
  const calls = JSON.stringify(core.calls());
  await stats({ buckets: bounds, alerts: ["sub_arch"] });
  assert.equal(JSON.stringify(core.calls()), calls);

  // Topics come from every title of the period, the older ones included.
  const period = new URLSearchParams({ after: bounds[2], before: bounds[0] });
  const topics = await until(() => get(`/demo/feed/topics?${period}`), (d) => !d.building, "titles read");
  assert.deepEqual(topics.items.find((x) => x.label === "Archives"), { label: "Archives", count: 120 });
  period.append("source", "Dépêches exemple");
  const narrowed = await get(`/demo/feed/topics?${period}`);
  assert.equal(narrowed.items.some((x) => x.label === "Archives"), false);

  // The Sources page's numbers, and the alerts' catches dated by the index.
  const sources = await get(`/demo/sources/stats?bounds=${bounds.join(",")}`);
  assert.deepEqual(sources.sources["Revue technique"].days, [0, 120]);
  assert.deepEqual(
    [sources.sources["Revue technique"].all, sources.sources["Revue technique"].caught],
    [120, 120],
  );
  const alerts = await get("/demo/alerts");
  assert.equal(Object.keys(alerts.dated.records).length, 120);
  assert.deepEqual(alerts.dated.namespaces, ["Revue technique"]);
  // Arrivals count from the oldest article indexed, or from when the demo
  // created the alert.
  assert.equal(alerts.dated.oldest, iso(now - 3 * DAY - 120 * 60000));
  const arrived = Object.fromEntries(alerts.items.map((a) => [a.alert_id, a.arrived]));
  assert.deepEqual(arrived, { sub_arch: 1120, sub_new: 1000 });

  // A correction moves an older article to today; a withdrawal removes one.
  core.correct("old0", "Archives municipales : lot corrigé");
  await until(() => stats({ buckets: bounds }).then((r) => r.data.buckets), (b) => b[0] === 1001, "corrected");
  core.withdraw("new0");
  const after = await until(() => stats({ buckets: bounds }).then((r) => r.data), (d) => d.total === 1119, "withdrawn");
  assert.deepEqual(after.buckets, [1000, 119]);

  for (const bad of [{ buckets: [...bounds].reverse() }, { read: "some" }, { sources: "Revue technique" }])
    assert.equal((await stats(bad)).status, 422, JSON.stringify(bad));
  assert.equal((await fetch(`${base}/demo/feed/topics`)).status, 422);
  // Topics cover a week or a day: a longer period would read too many titles.
  const long = new URLSearchParams({ after: iso(now - 9 * DAY), before: iso(now) });
  assert.equal((await fetch(`${base}/demo/feed/topics?${long}`)).status, 422);
});

test("the corpora the demo reads: search, feed and Explorer span them, any other is refused", async (t) => {
  // Two corpora read (demo, wires), one not (private). wires maps its own
  // filterable field, desk; the demo corpus maps none.
  const corpora = {
    demo: { corpus_id: "demo", name: "Espace démo", effective_retrieval: { fields: [] } },
    wires: {
      corpus_id: "wires",
      name: "Dépêches",
      effective_retrieval: {
        fields: [
          { name: "desk", type: "string", source_pointer: "/extensions/wire.item/data/desk", roles: ["filter"] },
          { name: "slug", type: "string", source_pointer: "/extensions/wire.item/data/slug", roles: ["search"] },
        ],
      },
    },
  };
  const records = {
    rec_note: { corpus: "demo", version: "v_note", at: "2026-10-05T08:00:00Z", text: "Port : note du matin", language: "fr" },
    rec_wire: { corpus: "wires", version: "v_wire2", at: "2026-10-05T09:00:00Z", text: "Port reopens", language: "en", desk: "economy", blobs: ["blob_1"] },
    rec_secret: { corpus: "private", version: "v_secret", at: "2026-10-05T10:00:00Z", text: "Hidden" },
  };
  const version = (id, r, text = r.text) => ({
    record_id: id,
    version_id: r.version,
    accepted_at: r.at,
    manifest: { parts: [{ key: "title", role: "title", content: { kind: "text", text } }] },
    extensions: {
      "quivr.metadata": { schema_version: "1", data: { language: r.language } },
      ...(r.desk ? { "wire.item": { schema_version: "1", data: { desk: r.desk } } } : {}),
    },
    provenance: { source_blob_ids: r.blobs || [] },
  });
  const listings = [];
  const searches = [];
  const counts = [];
  const upstream = http.createServer(async (req, res) => {
    const url = new URL(req.url, "http://x");
    const json = (status, data) => {
      res.writeHead(status, { "Content-Type": "application/json" });
      res.end(JSON.stringify(data));
    };
    if (url.pathname === "/v0/changes/stream") {
      // The second corpus's change stream is down: its feed is not live.
      if (url.searchParams.get("corpus_id") === "wires") return json(503, { code: "unavailable" });
      res.writeHead(200, { "Content-Type": "text/event-stream" });
      res.write(": live\n\n");
      return;
    }
    if (url.pathname === "/v0/changes") return json(200, { items: [], next_cursor: "c0", has_more: false });
    if (url.pathname === "/v0/search") {
      const chunks = [];
      for await (const chunk of req) chunks.push(chunk);
      searches.push(JSON.parse(Buffer.concat(chunks).toString("utf8")));
      return json(200, { items: [], retrieval_profile: { name: "default", version: "v" } });
    }
    if (url.pathname === "/v0/facets") {
      // One desk value; the demo corpus lacks desk, when it is filtered on.
      const chunks = [];
      for await (const chunk of req) chunks.push(chunk);
      const body = JSON.parse(Buffer.concat(chunks).toString("utf8"));
      counts.push(body);
      const desk = body.filter?.metadata?.some((p) => p.field === "desk");
      return json(200, {
        items: body.fields.map(({ field }) => ({ field, buckets: field === "desk" ? [{ value: "economy", count: 1 }] : [] })),
        ...(desk && body.corpus_ids.includes("demo") ? { excluded_corpora: [{ corpus_id: "demo", fields: ["desk"] }] } : {}),
      });
    }
    const corpus = url.pathname.match(/^\/v0\/corpora\/(\w+)$/);
    if (corpus) return corpora[corpus[1]] ? json(200, corpora[corpus[1]]) : json(404, { code: "not_found" });
    if (url.pathname === "/v0/blobs/blob_1")
      return json(200, { blob_id: "blob_1", size_bytes: 2048, sha256: "ab".repeat(32), media_type: "application/xml" });
    // Records accepted within the asked bounds, if any.
    const after = Date.parse(url.searchParams.get("accepted_after") || "") || -Infinity;
    const before = Date.parse(url.searchParams.get("accepted_before") || "") || Infinity;
    const within = (r) => Date.parse(r.at) >= after && Date.parse(r.at) < before;
    if (url.pathname === "/v0/records/count") {
      const corpus = url.searchParams.get("corpus_id");
      return json(200, { count: Object.values(records).filter((r) => r.corpus === corpus && within(r)).length });
    }
    if (url.pathname === "/v0/records") {
      listings.push(Object.fromEntries(url.searchParams));
      const asked = (url.searchParams.get("corpus_ids") || url.searchParams.get("corpus_id")).split(",");
      const filtered = url.searchParams.has("metadata");
      return json(200, {
        items: Object.entries(records)
          .filter(([, r]) => asked.includes(r.corpus) && within(r) && (!filtered || r.corpus !== "demo"))
          .sort(([, a], [, b]) => b.at.localeCompare(a.at))
          .map(([record_id, r]) => ({
            record_id,
            source: { corpus_id: r.corpus, namespace: "wire", record_key: record_id },
            withdrawn: false,
            current_version_id: r.version,
          })),
        ...(filtered && asked.includes("demo") ? { excluded_corpora: [{ corpus_id: "demo", fields: ["desk"] }] } : {}),
      });
    }
    const match = url.pathname.match(/^\/v0\/records\/(\w+)(?:\/versions\/(\w+))?$/);
    const record = match && records[match[1]];
    if (!record) return json(404, { code: "not_found" });
    if (match[2] === "v_wire1") return json(200, version(match[1], { ...record, version: "v_wire1", at: "2026-10-05T07:00:00Z" }, "Port closed"));
    if (match[2]) return json(200, version(match[1], record));
    json(200, {
      record_id: match[1],
      source: { corpus_id: record.corpus, namespace: "wire", record_key: match[1] },
      withdrawn: false,
      current_version_id: record.version,
    });
  });
  upstream.listen(0, "127.0.0.1");
  await once(upstream, "listening");
  t.after(() => {
    upstream.closeAllConnections();
    upstream.close();
  });
  const base = await startDemo(t, upstream.address().port, { QUIVR_DEMO_CORPORA: "wires" });
  const get = async (path) => {
    const response = await fetch(base + path);
    return { status: response.status, data: await response.json() };
  };

  // The corpora read, with the common fields and each one's filterable own fields.
  const listed = (await get("/demo/corpora")).data.items;
  assert.deepEqual(listed.map((c) => [c.corpus_id, c.name, c.demo]), [["demo", "Espace démo", true], ["wires", "Dépêches", false]]);
  assert.ok(listed[1].common.some((f) => f.name === "metadata.language"));
  assert.deepEqual(listed[1].own.map((f) => f.name), ["desk"]);

  // Search spans the corpora read; one more corpus is refused before the core.
  const search = (corpus_ids) =>
    fetch(base + "/v0/search", {
      method: "POST",
      headers: { "Content-Type": "application/json", Origin: base },
      body: JSON.stringify({ query: "port", mode: "lexical", limit: 10, corpus_ids }),
    });
  assert.equal((await search(["demo", "wires"])).status, 200);
  assert.equal((await search(["wires", "private"])).status, 403);
  assert.deepEqual(searches.map((s) => s.corpus_ids), [["demo", "wires"]]);

  // The Explorer lists newest first with the predicates as sent, each row with
  // its fields' values, and relays the corpora a field excluded.
  const predicates = [{ field: "desk", any_of: ["economy"] }];
  const page = await get(`/demo/explore?corpora=demo,wires&metadata=${encodeURIComponent(JSON.stringify(predicates))}`);
  assert.equal(page.status, 200);
  assert.deepEqual(listings.at(-1), {
    corpus_ids: "demo,wires",
    order: "accepted_at_desc",
    limit: "25",
    metadata: JSON.stringify(predicates),
  });
  assert.deepEqual(page.data.items.map((i) => [i.record_id, i.corpus_id, i.metadata]), [
    ["rec_wire", "wires", { "metadata.language": ["en"], desk: ["economy"] }],
  ]);
  assert.deepEqual(page.data.excluded_corpora, [{ corpus_id: "demo", fields: ["desk"] }]);
  // The engine counts the facets under the same corpora and predicates, and
  // its exclusions are relayed. A corpus's own fields are counted only when it
  // is picked alone, and a field's own filter is left out of its count.
  const filtered = `metadata=${encodeURIComponent(JSON.stringify(predicates))}`;
  const both = await get(`/demo/explore/facets?corpora=demo,wires&${filtered}`);
  assert.deepEqual(both.data.excluded_corpora, [{ corpus_id: "demo", fields: ["desk"] }]);
  const alone = await get(`/demo/explore/facets?corpora=wires&${filtered}`);
  assert.deepEqual(
    counts.map((b) => [b.corpus_ids, b.filter?.metadata, b.fields.map((f) => f.field).filter((f) => !f.startsWith("metadata."))]),
    [
      [["demo", "wires"], predicates, []],
      [["wires"], predicates, []],
      [["wires"], undefined, ["desk"]],
    ],
  );
  assert.deepEqual(counts[0].fields.find((f) => f.field === "metadata.published_at"), {
    field: "metadata.published_at",
    limit: 100,
    interval: "month",
  });
  assert.deepEqual(alone.data.fields.find((f) => f.field === "desk"), {
    field: "desk",
    type: "string",
    values: [{ value: "economy", count: 1 }],
  });
  for (const path of [
    "/demo/explore?corpora=private",
    "/demo/explore/facets?corpora=wires,private",
    "/demo/feed?corpora=demo,private",
  ])
    assert.equal((await get(path)).status, 403, path);
  assert.equal((await get(`/demo/explore?metadata=${encodeURIComponent('[{"field":"Bad"}]')}`)).status, 422);

  // One document: its current Version, the Versions the demo read, its
  // corpus's fields and its source files described. Others are not found.
  assert.equal((await get("/v0/records/rec_wire/versions/v_wire1")).status, 200);
  const doc = await get("/demo/explore/records/rec_wire");
  assert.equal(doc.data.version.version_id, "v_wire2");
  assert.deepEqual(doc.data.versions.map((v) => v.version_id), ["v_wire2", "v_wire1"]);
  assert.equal(doc.data.corpus.corpus_id, "wires");
  assert.deepEqual(doc.data.blobs.map((b) => b.media_type), ["application/xml"]);
  assert.equal((await get("/demo/explore/records/rec_secret")).status, 404);
  assert.equal((await get("/v0/records/rec_secret")).status, 404);

  // The feed merges the corpora picked, newest first.
  const feed = await get("/demo/feed?corpora=demo,wires");
  assert.deepEqual(feed.data.items.map((i) => [i.record_id, i.corpus_id]), [["rec_wire", "wires"], ["rec_note", "demo"]]);
  assert.deepEqual((await get("/demo/feed")).data.items.map((i) => i.record_id), ["rec_note"]);
  // Its numbers too: each corpus's day counts and index summed, one listing
  // for a day's page, and topics over both corpora's titles.
  const day = { after: "2026-10-05T00:00:00Z", before: "2026-10-06T00:00:00Z" };
  const days = await get(`/demo/feed/days?corpora=demo,wires&bounds=${day.before},${day.after}`);
  assert.deepEqual([days.data.total, days.data.days, days.data.older], [2, [2], 0]);
  const dayPage = await get(`/demo/feed/page?corpora=demo,wires&after=${day.after}&before=${day.before}`);
  assert.deepEqual(dayPage.data.items.map((i) => i.record_id), ["rec_wire", "rec_note"]);
  assert.equal(listings.at(-1).corpus_ids, "demo,wires");
  const stats = await fetch(base + "/demo/feed/stats?corpora=demo,wires", {
    method: "POST",
    headers: { "Content-Type": "application/json", Origin: base },
    body: JSON.stringify({ ...day, buckets: [day.before, day.after], read: "all" }),
  }).then((r) => r.json());
  assert.deepEqual([stats.total, stats.buckets, stats.sources.wire], [2, [2], 2]);
  const topics = await get(`/demo/feed/topics?corpora=demo,wires&after=${day.after}&before=${day.before}`);
  assert.ok(
    topics.data.items.some((topic) => topic.label.toLowerCase() === "port" && topic.count === 2),
    JSON.stringify(topics.data.items),
  );

  // The merged stream is live only when every corpus's feed is: the demo
  // corpus's alone goes live, both together stay off while one is down.
  const statusOf = async (path, wanted) => {
    const response = await fetch(base + path, { signal: AbortSignal.timeout(5000) });
    const reader = response.body.getReader();
    let text = "";
    try {
      for (;;) {
        const { value, done } = await reader.read();
        if (done) return undefined;
        text += new TextDecoder().decode(value);
        const statuses = [...text.matchAll(/event: status\ndata: (.*)\n/g)].map((m) => JSON.parse(m[1]).live);
        if (statuses.length && (wanted === undefined || statuses.includes(wanted))) return statuses.at(-1);
      }
    } finally {
      await reader.cancel();
    }
  };
  assert.equal(await statusOf("/demo/feed/stream", true), true);
  assert.equal(await statusOf("/demo/feed/stream?corpora=demo,wires"), false);

  // Another corpus is read, never written to.
  const write = await fetch(base + "/v0/records", {
    method: "POST",
    headers: { "Content-Type": "application/json", Origin: base },
    body: JSON.stringify({ source: { corpus_id: "wires", namespace: "web-demo", record_key: "k" }, content: { kind: "text", text: "x" } }),
  });
  assert.equal(write.status, 403);
});
