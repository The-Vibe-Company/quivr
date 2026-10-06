import { test, expect } from "@playwright/test";
import type {
  PluginCallStats,
  StatsList,
  StepStats,
} from "../src/lib/adminStats";

// The Goulots and Plugins sections against the real core (make verify-demo,
// THE-797): a text added through the facade is timed at each step by the
// worker's rollups, and the ingestion plugin that cut it shows as healthy
// with its calls. Search readiness precedes the asynchronous stats flush;
// wait for published rollups before Admin takes its first snapshot.
// Synthetic content only.
const run = Date.now().toString(36);

test.beforeEach(async ({ page }) => {
  await page.request.post("/demo/login", {
    data: { password: process.env.QUIVR_DEMO_PASSWORD || "local-browser-demo" },
  });
});

test("un texte ajouté est chronométré à chaque étape, et le plugin d’ingestion est opérationnel", async ({
  page,
}, info) => {
  const { corpus_id } = await (await page.request.get("/demo/session")).json();
  const key = `admin-health-${run}`;
  // The facade takes writes from its own origin only, as the app sends them.
  await page.goto("/");
  const added = await page.evaluate(
    async (body) => {
      const response = await fetch("/v0/records", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      return { status: response.status, data: await response.json() };
    },
    {
      idempotency_key: key,
      source: { corpus_id, namespace: "web-demo", record_key: key },
      content: {
        kind: "text",
        text: `Relevé chronométré ${run}\nUn texte pour mesurer chaque étape.`,
      },
    },
  );
  expect(added.status, JSON.stringify(added.data)).toBeLessThan(300);
  const receipt = added.data.receipt_id;
  await expect
    .poll(
      async () =>
        (
          await (
            await page.request.get(`/v0/ingestion-receipts/${receipt}`)
          ).json()
        ).availability?.searchable,
    )
    .toBe(true);

  await page.setViewportSize({ width: 1440, height: 1000 });
  // Force the first stats read ahead of publication. Subsequent reads use
  // the real facade and worker rollups, including their cache.
  let firstStepsRead = true;
  await page.route("**/demo/admin/stats/steps?window=1h", async (route) => {
    if (firstStepsRead) {
      firstStepsRead = false;
      await route.fulfill({ json: { items: [] } });
    } else await route.continue();
  });
  await expect
    .poll(
      () =>
        page.evaluate(async () => {
          const read = async <T>(kind: string): Promise<StatsList<T>> => {
            const response = await fetch(`/demo/admin/stats/${kind}?window=1h`);
            if (!response.ok)
              throw new Error(`${kind}: HTTP ${response.status}`);
            return response.json();
          };
          const [steps, plugins] = await Promise.all([
            read<StepStats>("steps"),
            read<PluginCallStats>("plugins"),
          ]);
          return {
            timed: ["materialized", "segmented", "retrieval_ready"].filter(
              (name) =>
                steps.items.some(
                  (s) =>
                    s.step === name &&
                    s.summary.count > 0 &&
                    s.summary.p50_ms !== undefined,
                ),
            ),
            ingestion: plugins.items.some(
              (p) =>
                p.plugin_id === "core.ingest" &&
                p.operation === "segment_and_embed" &&
                p.summary.count > 0,
            ),
          };
        }),
      // A just-cached empty rollup lasts 10 s. Leave room to observe its
      // replacement while keeping the whole spec within the 15 s budget.
      { timeout: 12000, message: "Published step timings and ingestion calls" },
    )
    .toEqual({
      timed: ["materialized", "segmented", "retrieval_ready"],
      ingestion: true,
    });
  await page.goto("/?view=admin");
  const necks = page.getByRole("region", { name: "Goulots par étape" });
  // Each step's card gives its p50 first.
  for (const name of ["Reçu", "Découpé", "Trouvable"])
    await expect(
      necks
        .getByRole("listitem")
        .filter({ has: page.getByRole("heading", { name, exact: true }) })
        .getByRole("definition")
        .first(),
      name,
    ).toHaveText(/\d\s?(ms|s|min)$/);
  await expect(necks.getByRole("status")).not.toBeEmpty();

  await page.getByRole("tab", { name: "Plugins" }).click();
  const ingest = page
    .getByRole("region", { name: "Plugins" })
    .getByRole("listitem")
    .filter({ hasText: "core.ingest" });
  await expect(ingest.getByRole("button")).toContainText("Opérationnel");
  await expect(ingest.getByRole("button")).toContainText(
    "Découpage et vecteurs",
  );
  await page.screenshot({
    path: info.outputPath("admin-health.png"),
    fullPage: true,
  });
});
