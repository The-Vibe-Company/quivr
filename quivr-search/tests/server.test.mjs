import { test } from "node:test";
import assert from "node:assert/strict";
import http from "node:http";
import { spawn } from "node:child_process";
import { once } from "node:events";
import { setTimeout as delay } from "node:timers/promises";

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
  const demo = spawn(process.execPath, ["server.mjs"], {
    env: {
      ...process.env,
      HOST: "127.0.0.1",
      PORT: "0",
      DEMO_PASSWORD: "",
      QUIVR_API_URL: `http://127.0.0.1:${upstream.address().port}`,
      QUIVR_API_KEY: "fixture-server-key",
      QUIVR_DEMO_CORPUS_ID: "demo",
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
  for (const route of [
    "/v0/records/known",
    "/v0/records/known/versions/version",
    "/v0/ingestion-receipts/known",
  ]) {
    const response = await fetch(`http://127.0.0.1:${port}${route}`);
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
      (await fetch(`http://127.0.0.1:${port}${route}`)).status,
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

test("sources: suggestions, guarded discovery and creation, removal hides every instance", async (t) => {
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
});
