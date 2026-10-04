import { test, expect } from "@playwright/test";

// The Goulots and Plugins sections against the real core (make verify-demo,
// THE-797): a text added through the facade is timed at each step by the
// worker's rollups, and the ingestion plugin that cut it shows as healthy
// with its calls. Over the 15 s budget only when the facade's 10 s cache and
// the section's 15 s refresh both just missed the text: the waits are
// bounded conditions, never sleeps. Synthetic content only.
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
      { timeout: 30000 },
    )
    .toBe(true);

  await page.setViewportSize({ width: 1440, height: 1000 });
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
    ).toHaveText(/\d\s?(ms|s|min)$/, { timeout: 30000 });
  await expect(necks.getByRole("status")).not.toBeEmpty();

  await page.getByRole("tab", { name: "Plugins" }).click();
  const ingest = page
    .getByRole("region", { name: "Plugins" })
    .getByRole("listitem")
    .filter({ hasText: "core.ingest" });
  await expect(ingest.getByRole("button")).toContainText("Opérationnel", {
    timeout: 30000,
  });
  await expect(ingest.getByRole("button")).toContainText(
    "Découpage et vecteurs",
  );
  await page.screenshot({
    path: info.outputPath("admin-health.png"),
    fullPage: true,
  });
});
