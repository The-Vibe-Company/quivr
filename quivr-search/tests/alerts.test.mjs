// The demo facade's keyword alert routes (alerts.mjs) against a small fake
// core. The real flow, with the alerts plugin deciding, is tests/alerts.spec.ts.
import { test } from "node:test";
import assert from "node:assert/strict";
import http from "node:http";
import { spawn } from "node:child_process";
import { once } from "node:events";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { setTimeout as delay } from "node:timers/promises";

const KEY = "fixture-server-key";
const OWNER = "quivr-web-demo";

// A fake core holding Saved Query Versions, Subscriptions, Matches and Records.
function fakeCore() {
  const seen = [];
  const queries = new Map(); // version id → SavedQueryVersion
  const subs = new Map();
  const matches = [];
  const records = new Map();
  let refuse = null;
  let forbidden = false;
  let n = 0;
  const query = (corpus, match) => {
    const version = {
      saved_query_id: `sq${++n}`,
      version_id: `sqv${n}`,
      definition: {
        corpus_ids: [corpus],
        expression: { kind: "keywords", match },
      },
    };
    queries.set(version.version_id, version);
    return version;
  };
  const subscription = (id, owner, version, enabled = true) =>
    subs.set(id, {
      subscription_id: id,
      name: id,
      enabled,
      deleted: false,
      owner,
      current_version: {
        subscription_id: id,
        version_id: `${id}-v1`,
        saved_query_id: version.saved_query_id,
        saved_query_version_id: version.version_id,
        evaluator: {},
        destination_id: "d",
      },
    });
  const server = http.createServer(async (req, res) => {
    const chunks = [];
    for await (const chunk of req) chunks.push(chunk);
    const body = chunks.length ? JSON.parse(Buffer.concat(chunks)) : undefined;
    seen.push({
      method: req.method,
      url: req.url,
      body,
      auth: req.headers.authorization,
    });
    const url = new URL(req.url, "http://x");
    const send = (status, data) => {
      res.writeHead(status, { "Content-Type": "application/json" });
      res.end(JSON.stringify(data));
    };
    const p = url.pathname;
    let m;
    if (p === "/v0/subscriptions" && req.method === "GET") {
      if (forbidden)
        return send(403, {
          code: "forbidden",
          message: "missing monitoring:read",
        });
      const owner = url.searchParams.get("owner");
      return send(200, {
        items: [...subs.values()].filter(
          (s) => s.owner === owner && s.enabled && !s.deleted,
        ),
      });
    }
    if (p === "/v0/saved-queries" && req.method === "POST") {
      const version = query(
        body.definition.corpus_ids[0],
        body.definition.expression.match,
      );
      version.definition = body.definition;
      return send(201, {
        saved_query_id: version.saved_query_id,
        name: body.name,
        deleted: false,
        current_version: version,
      });
    }
    if ((m = p.match(/^\/v0\/saved-queries\/(\w+)\/versions\/(\w+)$/)))
      return queries.has(m[2])
        ? send(200, queries.get(m[2]))
        : send(404, { code: "not_found" });
    if ((m = p.match(/^\/v0\/saved-queries\/(\w+)\/delete$/)))
      return send(200, { saved_query_id: m[1], deleted: true });
    if (p === "/v0/subscriptions" && req.method === "POST") {
      if (refuse) return send(422, refuse);
      const id = `sub${++n}`;
      subs.set(id, {
        subscription_id: id,
        name: body.name,
        enabled: true,
        deleted: false,
        owner: body.owner,
        current_version: {
          subscription_id: id,
          version_id: `${id}-v1`,
          saved_query_id: body.saved_query_id,
          saved_query_version_id: body.saved_query_version_id,
          evaluator: body.evaluator,
          destination_id: body.destination_id,
        },
      });
      return send(201, subs.get(id));
    }
    if (
      (m = p.match(
        /^\/v0\/subscriptions\/(\w+)(?:\/(disable|enable|delete))?$/,
      ))
    ) {
      const sub = subs.get(m[1]);
      if (!sub) return send(404, { code: "not_found" });
      if (m[2] === "disable") sub.enabled = false;
      if (m[2] === "enable") sub.enabled = true;
      if (m[2] === "delete")
        Object.assign(sub, { enabled: false, deleted: true });
      return send(200, sub);
    }
    if (p === "/v0/matches")
      return send(200, {
        items: matches.filter(
          (x) => x.subscription_id === url.searchParams.get("subscription_id"),
        ),
      });
    if ((m = p.match(/^\/v0\/records\/(\w+)(?:\/versions\/(\w+))?$/))) {
      const record = records.get(m[1]);
      if (!record) return send(404, { code: "not_found" });
      return send(
        200,
        m[2]
          ? record.version
          : { record_id: m[1], source: record.source, withdrawn: false },
      );
    }
    send(404, { code: "not_found", message: p });
  });
  return {
    server,
    seen,
    subs,
    matches,
    records,
    query,
    subscription,
    refuse: (error) => (refuse = error),
    forbid: () => (forbidden = true),
  };
}

async function start(t, env = {}) {
  const core = fakeCore();
  core.server.listen(0, "127.0.0.1");
  await once(core.server, "listening");
  const dir = await mkdtemp(join(tmpdir(), "quivr-alerts-"));
  const demo = spawn(process.execPath, ["server.mjs"], {
    env: {
      ...process.env,
      HOST: "127.0.0.1",
      PORT: "0",
      DEMO_PASSWORD: "",
      QUIVR_API_URL: `http://127.0.0.1:${core.server.address().port}`,
      QUIVR_API_KEY: KEY,
      QUIVR_DEMO_CORPUS_ID: "demo",
      QUIVR_DEMO_DESTINATION_ID: "demo-destination",
      QUIVR_DEMO_ALERTS_EVALUATOR: "alerts@9.9.9",
      DEMO_STATE_FILE: join(dir, "state.json"),
      ...env,
    },
    stdio: ["ignore", "pipe", "pipe"],
  });
  t.after(async () => {
    if (demo.exitCode === null) {
      demo.kill();
      await once(demo, "exit");
    }
    core.server.closeAllConnections();
    core.server.close();
    await rm(dir, { recursive: true, force: true });
  });
  const ready = await Promise.race([
    once(demo.stdout, "data"),
    delay(5000, undefined, { ref: false }).then(() => {
      throw new Error("demo startup timeout");
    }),
  ]);
  const base = `http://127.0.0.1:${String(ready[0]).match(/:(\d+)/)[1]}`;
  const call = async (path, body) => {
    const response = await fetch(
      base + path,
      body === undefined
        ? {}
        : {
            method: "POST",
            headers: { Origin: base, "Content-Type": "application/json" },
            body: JSON.stringify(body),
          },
    );
    const text = await response.text();
    assert.ok(!text.includes(KEY), "the core key never reaches the browser");
    return { status: response.status, data: JSON.parse(text) };
  };
  return { core, call };
}

const idem = () => `k-${Math.random().toString(36).slice(2, 12)}`;

test("an alert is created in the demo corpus with the deployment's evaluator, destination and owner; paused, it stays listed", async (t) => {
  const { core, call } = await start(t);
  const created = await call("/demo/alerts", {
    idempotency_key: idem(),
    name: "Orages",
    expression: { kind: "keywords", match: { term: "orage" } },
    corpus_ids: ["private-corpus"],
  });
  assert.equal(created.status, 201);
  const [saved] = core.seen.filter((r) => r.url === "/v0/saved-queries");
  assert.deepEqual(saved.body.definition.corpus_ids, ["demo"]);
  const [sub] = core.seen.filter(
    (r) => r.url === "/v0/subscriptions" && r.method === "POST",
  );
  assert.deepEqual(sub.body.evaluator, {
    plugin_id: "alerts",
    version: "9.9.9",
    configuration: {},
  });
  assert.equal(sub.body.destination_id, "demo-destination");
  assert.equal(sub.body.owner, OWNER);
  assert.equal(sub.auth, `Bearer ${KEY}`);

  const id = created.data.alert_id;
  const paused = await call(`/demo/alerts/${id}/pause`, {
    idempotency_key: idem(),
  });
  assert.equal(paused.status, 200);
  assert.equal(paused.data.enabled, false);
  // The core lists only active Subscriptions; the facade still shows the paused one.
  const listed = await call("/demo/alerts");
  assert.deepEqual(
    listed.data.items.map((a) => [a.alert_id, a.enabled]),
    [[id, false]],
  );

  const deleted = await call(`/demo/alerts/${id}/delete`, {
    idempotency_key: idem(),
  });
  assert.equal(deleted.status, 200);
  assert.deepEqual((await call("/demo/alerts")).data.items, []);
});

test("alerts of another owner or another corpus are not the demo's: reads and actions are 404 and never relayed", async (t) => {
  const { core, call } = await start(t);
  core.subscription(
    "foreign",
    "someone-else",
    core.query("demo", { term: "x" }),
  );
  core.subscription(
    "elsewhere",
    OWNER,
    core.query("private-corpus", { term: "x" }),
  );
  for (const id of ["foreign", "elsewhere"]) {
    assert.equal((await call(`/demo/alerts/${id}`)).status, 404, id);
    const before = core.seen.length;
    const paused = await call(`/demo/alerts/${id}/pause`, {
      idempotency_key: idem(),
    });
    assert.equal(paused.status, 404, id);
    assert.ok(
      core.seen.slice(before).every((r) => r.method === "GET"),
      `${id}: no mutation relayed`,
    );
  }
  // The active "elsewhere" alert of the owner is not listed either.
  assert.deepEqual((await call("/demo/alerts")).data.items, []);
});

test("an alert's page shows each caught article once, newest first, with the matched words and where they were found", async (t) => {
  const { core, call } = await start(t);
  core.subscription("mine", OWNER, core.query("demo", { term: "orage" }));
  const article = (id, title, body) =>
    core.records.set(id, {
      source: { corpus_id: "demo", namespace: "web-demo" },
      version: {
        record_id: id,
        version_id: `${id}v`,
        manifest: {
          parts: [
            { key: "t", role: "title", content: { kind: "text", text: title } },
            { key: "b", role: "body", content: { kind: "text", text: body } },
          ],
        },
      },
    });
  const filler =
    "Le marché du samedi s’est tenu comme chaque semaine sur la place. ".repeat(
      4,
    );
  article("r1", "Un orage sur la côte", "Rien à signaler.");
  article(
    "r2",
    "Bulletin du soir",
    `${filler}Un violent Orage a éclaté vers minuit.`,
  );
  const caught = (match_id, record_id, terms) =>
    core.matches.push({
      match_id,
      subscription_id: "mine",
      record_id,
      record_version_id: `${record_id}v`,
      evidence: {
        explanation: "Matched",
        details: { kind: "keywords", terms, fields: [] },
      },
    });
  caught("m1", "r1", [{ term: "orage", part_keys: ["t"] }]);
  caught("m2", "r2", [{ term: "orage", part_keys: ["b"] }]);
  caught("m3", "r1", [{ term: "orage", part_keys: ["t", "b"] }]); // a newer Match of r1

  const page = await call("/demo/alerts/mine");
  assert.equal(page.status, 200);
  assert.equal(page.data.match_count, 2);
  assert.deepEqual(
    page.data.matches.map((m) => [m.match_id, m.title, m.source]),
    [
      ["m3", "Un orage sur la côte", "web-demo"],
      ["m2", "Bulletin du soir", "web-demo"],
    ],
  );
  assert.deepEqual(page.data.matches[0].terms, [
    { term: "orage", parts: ["title", "body"] },
  ]);
  // The excerpt starts near the matched word, not at the top of a long text.
  assert.match(
    page.data.matches[1].excerpt,
    /^… .*Orage a éclaté vers minuit\.$/,
  );
  assert.deepEqual((await call("/demo/alerts")).data.matched, {
    r1: ["mine"],
    r2: ["mine"],
  });
});

test("a query the evaluator refuses is explained and leaves no saved search behind", async (t) => {
  const { core, call } = await start(t);
  core.refuse({
    code: "invalid_expression",
    message: "/match: not valid",
    field: "/saved_query_version_id",
  });
  const created = await call("/demo/alerts", {
    idempotency_key: idem(),
    name: "Refusée",
    expression: { kind: "keywords", match: { term: "" } },
  });
  assert.equal(created.status, 422);
  assert.equal(created.data.code, "invalid_expression");
  assert.ok(
    core.seen.some((r) => /^\/v0\/saved-queries\/sq\d+\/delete$/.test(r.url)),
  );
  assert.deepEqual((await call("/demo/alerts")).data.items, []);
});

test("without monitoring rights or a destination, the page learns that alerts are not enabled", async (t) => {
  const forbidden = await start(t);
  forbidden.core.forbid();
  assert.deepEqual((await forbidden.call("/demo/alerts")).data, {
    available: false,
    items: [],
    matched: {},
  });
  const unconfigured = await start(t, { QUIVR_DEMO_DESTINATION_ID: "" });
  assert.equal((await unconfigured.call("/demo/alerts")).data.available, false);
  const create = await unconfigured.call("/demo/alerts", {
    idempotency_key: idem(),
    name: "x",
    expression: { kind: "keywords", match: { term: "x" } },
  });
  assert.equal(create.status, 404);
  assert.equal(unconfigured.core.seen.length, 0);
});
