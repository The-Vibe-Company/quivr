import { test } from "node:test";
import assert from "node:assert/strict";
import { activePlugins } from "../admin-plugins.mjs";

// The Admin tab's plugin list relay (THE-797): it passes plugin ids,
// versions and roles only, explains a key without observability:read,
// reads a core without a plugin registry as running no plugin, and caches.
test("the active plugins relay keeps ids, versions and roles, and caches", async () => {
  let now = 0;
  let answer = {
    status: 200,
    data: {
      plan_activated_at: "2026-09-30T10:00:00Z",
      items: [
        {
          plugin_id: "core-ingest",
          version: "0.2.0",
          roles: ["ingestion"],
          endpoint: "http://10.0.0.5:9000",
        },
      ],
    },
  };
  const paths = [];
  const read = activePlugins({
    upstream: async (path) => {
      paths.push(path);
      return answer;
    },
    clock: () => now,
  });
  assert.deepEqual(await read(), {
    status: 200,
    data: {
      plan_activated_at: "2026-09-30T10:00:00Z",
      items: [
        { plugin_id: "core-ingest", version: "0.2.0", roles: ["ingestion"] },
      ],
    },
  });
  now = 9000;
  await read();
  assert.deepEqual(
    paths,
    ["/v0/admin/active-plugins"],
    "a second read within 10 s is served from the cache",
  );

  answer = { status: 403, data: { code: "forbidden" } };
  now = 20000;
  const refused = await read();
  assert.equal(refused.status, 403);
  assert.match(refused.data.message, /observability:read/);

  answer = { status: 404, data: { code: "not_found" } };
  now = 40000;
  assert.deepEqual(await read(), { status: 200, data: { items: [] } });
});
