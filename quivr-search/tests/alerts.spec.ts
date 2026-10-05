import { test, expect, type Page } from "@playwright/test";

// Alerts on the real local stack: the alerts plugin decides every new article
// of the demo corpus. Each run writes its own id into its query and its
// articles, so other tests' texts never match. Described alerts are judged by
// the fake System One server (alerts.fake_system_one), never by TypeSafe.
const FEEDS = process.env.QUIVR_DEMO_FEEDS_URL || "";
const run = `r${Date.now().toString(36)}`;

test.skip(!FEEDS, "needs the local test feeds (make verify-demo)");

test.beforeEach(async ({ page }) => {
  // Matches arrive through real ingestion and connector polls; described
  // alerts also wait for each article's enrichment (its embeddings).
  test.setTimeout(240000);
  await page.request.post("/demo/login", {
    data: { password: process.env.QUIVR_DEMO_PASSWORD || "local-browser-demo" },
  });
});

// The facade takes mutations from its own origin only, like the app sends them.
const sameOrigin = (page: Page) => ({ Origin: new URL(page.url()).origin });

/** Adds a text through the facade, as the "Ajouter du texte" dialog does, and waits until it is searchable. */
async function addText(page: Page, text: string, enriched = false) {
  const { corpus_id } = await (await page.request.get("/demo/session")).json();
  const key = `alert-${run}-${Math.random().toString(36).slice(2)}`;
  const receipt = await (
    await page.request.post("/v0/records", {
      headers: sameOrigin(page),
      data: {
        idempotency_key: key,
        source: { corpus_id, namespace: "web-demo", record_key: key },
        content: { kind: "text", text },
      },
    })
  ).json();
  await expect
    .poll(
      async () => {
        const status = await (
          await page.request.get(`/v0/ingestion-receipts/${receipt.receipt_id}`)
        ).json();
        if (status.availability?.searchable !== true) return false;
        if (!enriched) return true;
        const version = await (
          await page.request.get(`/v0/records/${receipt.record_id}/versions/${status.version_id}`)
        ).json();
        return Boolean(version.steps?.enriched_at);
      },
      { timeout: 60000 },
    )
    .toBe(true);
}

const alertRow = (page: Page, name: string) =>
  page
    .getByRole("table", { name: "Alertes" })
    .getByRole("row")
    .filter({ hasText: name });

test("une alerte par mots-clés montre ce qu’elle a trouvé, puis se met en pause, se modifie et se supprime", async ({
  page,
}, info) => {
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.goto("/?view=alerts");
  await addText(page, `Orage de grêle sur la côte ${run}, avant la création de l’alerte.`);
  // The form opens in a panel from the page's button.
  await page.getByRole("button", { name: "Nouvelle alerte" }).click();
  await expect(
    page.getByRole("heading", { name: "Nouvelle alerte" }),
  ).toBeVisible();

  // A query with groups is written in the advanced field, read as it is
  // typed; a mistake is explained before saving.
  await page.getByRole("button", { name: "Écrire une requête avancée" }).click();
  const query = page.getByLabel("Requête avancée");
  await query.fill("orage AND");
  await expect(page.locator("#alert-query-preview")).toHaveAttribute(
    "data-state",
    "invalid",
  );
  await query.fill(`${run} AND introuvable`);
  await expect(page.locator(".alert-preview-title")).toHaveText(/^Aucun sur les \d+ derniers$/);
  await query.fill(`${run} AND (orage OR grêle) NOT football`);
  const preview = page.locator("#alert-query-preview");
  await expect(preview).toHaveAttribute("data-state", "valid");
  await expect(preview.locator(".keyword")).toHaveText([
    run,
    "orage",
    "grêle",
    "football",
  ]);
  await expect(page.locator(".alert-preview-title")).toHaveText(/^1 article sur les \d+ derniers$/);
  await expect(page.getByRole("list", { name: "Articles qui auraient été attrapés" })).toContainText(`Orage de grêle sur la côte ${run}`);
  const name = `Orages ${run}`;
  await page.getByLabel("Nom de l’alerte").fill(name);
  await page.screenshot({
    path: info.outputPath("alerts-compose.png"),
    fullPage: true,
  });
  await page.getByRole("button", { name: "Créer l’alerte" }).click();
  const row = alertRow(page, name);
  await expect(row.getByTestId("alert-count")).toHaveText("0");
  // Saving closes the panel.
  await expect(page.getByRole("form", { name: "Nouvelle alerte" })).toHaveCount(0);

  // Only articles arriving after the alert count: a text without the words,
  // then one with them, then an RSS source with one article of each kind.
  await addText(page, `Le marché aux fleurs du port ${run} rouvre samedi.`);
  await addText(
    page,
    `Orage de grêle sur le port ${run} : les quais restent fermés jusqu’à demain.`,
  );
  const { corpus_id } = await (await page.request.get("/demo/session")).json();
  const source = await page.request.post("/v0/connectors", {
    headers: sameOrigin(page),
    data: {
      idempotency_key: `alerts-feed-${run}`,
      corpus_id,
      source_namespace: `vigie-${run}`,
      kind: "rss",
      config: { url: `${FEEDS}/feeds/alerts.xml?run=${run}` },
    },
  });
  expect(source.status()).toBe(201);

  // Both matches show live on the list, then on the alert's page, with the
  // words that matched and where.
  await expect(row.getByTestId("alert-count")).toHaveText("2", {
    timeout: 120000,
  });
  await row.getByRole("button", { name, exact: true }).click();
  await expect(page.getByRole("heading", { name, level: 2 })).toBeVisible();
  const caught = page.getByRole("list", { name: "Articles trouvés" });
  const typed = caught
    .locator(".caught")
    .filter({ hasText: `Orage de grêle sur le port ${run}` });
  const fed = caught
    .locator(".caught")
    .filter({ hasText: `Grêle et orage sur le vignoble ${run}` });
  await expect(typed.locator(".caught-source")).toContainText(
    "Ajouté à la main",
  );
  await expect(typed.getByRole("list", { name: "Mots trouvés" })).toContainText(
    "orage dans le texte",
  );
  await expect(fed.locator(".caught-source")).toContainText(`vigie-${run}`);
  await expect(fed.getByRole("list", { name: "Mots trouvés" })).toContainText(
    "orage dans le titre et le texte",
  );
  await expect(caught.locator(".caught")).toHaveCount(2);
  await expect(caught).not.toContainText("marché aux fleurs");
  await page.screenshot({
    path: info.outputPath("alerts-detail.png"),
    fullPage: true,
  });

  // Opening an article shows the whole text in the reader, over the alert's
  // page, with why the alert caught it.
  await fed.getByRole("button").first().click();
  const reader = page.getByRole("complementary", {
    name: new RegExp(`Grêle et orage sur le vignoble ${run}`),
  });
  await expect(reader).toContainText("couché les vignes");
  await expect(
    reader.getByRole("list", { name: "Pourquoi cet article" }),
  ).toContainText(`Attrapé par votre alerte « ${name} »`);
  await expect(page.getByRole("heading", { name, level: 2 })).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(reader).toHaveCount(0);
  await page
    .getByRole("navigation", { name: "Sections" })
    .getByRole("link", { name: /^Fil/ })
    .click();

  // In the feed, the caught articles carry the alert; the others do not.
  const feed = page.getByRole("list", { name: "Derniers éléments" });
  const marked = (text: string) =>
    feed
      .getByRole("listitem")
      .filter({ hasText: text })
      .getByRole("list", { name: "Alertes déclenchées" });
  await expect(marked(`Orage de grêle sur le port ${run}`)).toHaveText(name);
  await expect(marked(`Grêle et orage sur le vignoble ${run}`)).toHaveText(
    name,
  );
  await expect(
    feed
      .getByRole("listitem")
      .filter({ hasText: `Le marché aux fleurs du port ${run}` }),
  ).toBeVisible();
  await expect(marked(`Le marché aux fleurs du port ${run}`)).toHaveCount(0);
  await page.screenshot({
    path: info.outputPath("alerts-feed.png"),
    fullPage: true,
  });
  await page
    .getByRole("navigation", { name: "Sections" })
    .getByRole("link", { name: /^Alertes/ })
    .click();
  await row.getByRole("button", { name, exact: true }).click();
  await expect(page.getByRole("heading", { name, level: 2 })).toBeVisible();

  // Pause, resume, edit (applies to later articles) and delete.
  const actions = page.getByRole("group", { name: "Actions de l’alerte" });
  const active = actions.getByRole("switch", { name: "Alerte active" });
  await active.click();
  await expect(active).toHaveAttribute("aria-checked", "false");
  await expect(row.locator(".alert-state")).toHaveText("En pause");
  await active.click();
  await expect(row.locator(".alert-state")).toHaveText("Active");

  // The alert fits the guided form, so it opens there.
  await actions.getByRole("button", { name: "Modifier" }).click();
  const edit = page.getByRole("form", { name: "Modifier l’alerte" });
  await expect(edit.getByLabel("Au moins un de ces mots")).toHaveValue("orage grêle");
  await edit.getByLabel("Tous ces mots").fill(`${run} tempête`);
  await edit.getByLabel("Au moins un de ces mots").fill("");
  await edit.getByLabel("Aucun de ces mots").fill("");
  await edit.getByRole("button", { name: "Enregistrer" }).click();
  await expect(page.locator(".alert-sheet .sheet-rule .kw")).toHaveText([
    run,
    "tempête",
  ]);
  await expect(caught.locator(".caught")).toHaveCount(2);

  await actions
    .getByRole("button", { name: `Plus d’actions pour ${name}` })
    .click();
  await actions.getByRole("menuitem", { name: "Supprimer" }).click();
  await page
    .getByRole("group", { name: "Confirmer la suppression" })
    .getByRole("button", { name: "Supprimer définitivement" })
    .click();
  await expect(
    page.getByRole("heading", { name: "Alertes", level: 1 }),
  ).toBeVisible();
  await expect(alertRow(page, name)).toHaveCount(0);
});

test("une alerte décrite en langage courant trouve un article formulé autrement, avec son score", async ({
  page,
}, info) => {
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.goto("/?view=alerts");
  await addText(page, `Les dockers cessent le travail au port ${run}, avant la création de l’alerte : un débrayage bloque les navires.`, true);
  await page.getByRole("button", { name: "Nouvelle alerte" }).click();
  await page
    .getByRole("group", { name: "Type d’alerte" })
    .getByRole("button", { name: "Un sujet décrit" })
    .click();
  await expect(page.locator("#alert-described-note")).toContainText(
    "Le texte des articles est envoyé à ce service externe.",
  );
  await page
    .getByLabel("Décrivez le sujet en une phrase")
    .fill("Des grèves dans les ports");
  await page.getByRole("button", { name: "Tester sur les derniers articles" }).click();
  await expect(page.getByRole("list", { name: "Articles qui auraient été attrapés" })).toContainText(
    `Les dockers cessent le travail au port ${run}, avant la création`,
  );
  const name = `Ports ${run}`;
  await page.getByLabel("Nom de l’alerte").fill(name);
  await page.screenshot({
    path: info.outputPath("alerts-described-compose.png"),
    fullPage: true,
  });
  await page.getByRole("button", { name: "Créer l’alerte" }).click();
  const row = alertRow(page, name);
  // The new alert is selected: its sheet says what it looks for.
  await expect(page.locator(".alert-sheet .rule-quote")).toHaveText(
    "« Des grèves dans les ports »",
  );

  // No shared keyword: the classifier judges the meaning of each new article.
  await addText(page, `Le marché aux fleurs ${run} rouvre samedi.`);
  await addText(
    page,
    `Les dockers cessent le travail au port ${run} : un débrayage bloque les navires.`,
  );
  await row.getByRole("button", { name, exact: true }).click();
  await expect(page.getByRole("heading", { name, level: 2 })).toBeVisible();
  const caught = page.getByRole("list", { name: "Articles trouvés" });
  const item = caught
    .locator(".caught")
    .filter({ hasText: `Les dockers cessent le travail au port ${run}` });
  await expect(item.getByTestId("caught-score")).toHaveText("Score 0,92", {
    timeout: 120000,
  });
  // What the score means, and the alert's threshold, on hover or focus.
  await expect(item.getByRole("tooltip")).toContainText("Très pertinent");
  await expect(item.getByRole("tooltip")).toContainText("dépasse 0,50");
  await expect(caught).not.toContainText(`marché aux fleurs ${run}`);
  await page.screenshot({
    path: info.outputPath("alerts-described-detail.png"),
    fullPage: true,
  });

  // A described alert is edited and deleted like a keyword alert.
  const actions = page.getByRole("group", { name: "Actions de l’alerte" });
  await actions.getByRole("button", { name: "Modifier" }).click();
  const edit = page.getByRole("form", { name: "Modifier l’alerte" });
  await edit
    .getByLabel("Décrivez le sujet en une phrase")
    .fill("Des inondations après de fortes pluies");
  await edit.getByRole("button", { name: "Enregistrer" }).click();
  await expect(page.locator(".alert-sheet .rule-quote")).toHaveText(
    "« Des inondations après de fortes pluies »",
  );
  await actions
    .getByRole("button", { name: `Plus d’actions pour ${name}` })
    .click();
  await actions.getByRole("menuitem", { name: "Supprimer" }).click();
  await page
    .getByRole("group", { name: "Confirmer la suppression" })
    .getByRole("button", { name: "Supprimer définitivement" })
    .click();
  await expect(alertRow(page, name)).toHaveCount(0);
});

test("sans classifieur, la page ne propose que les mots-clés et dit pourquoi", async ({
  page,
}) => {
  // The deployment's answer as a facade without DEMO_DESCRIBED_ALERTS gives it.
  await page.route("**/demo/alerts", async (route) => {
    const response = await route.fetch();
    await route.fulfill({
      response,
      json: { ...(await response.json()), described: false },
    });
  });
  await page.goto("/?view=alerts");
  await page.getByRole("button", { name: "Nouvelle alerte" }).click();
  await expect(page.getByLabel("Tous ces mots")).toBeVisible();
  await expect(page.getByRole("group", { name: "Type d’alerte" })).toHaveCount(
    0,
  );
  await expect(page.locator(".alert-kind-off")).toContainText(
    "ne sont pas activées sur ce déploiement",
  );
  // The app reads the list twice on load; the second fetch may still be in flight.
  await page.unrouteAll({ behavior: "ignoreErrors" });
});

test("la page des alertes tient sur mobile, en mode clair et sombre", async ({
  page,
}, info) => {
  await page.setViewportSize({ width: 375, height: 812 });
  await page.goto("/?view=alerts");
  await page.getByRole("button", { name: "Nouvelle alerte" }).click();
  await page.getByLabel("Cette phrase exacte").fill("marché aux fleurs");
  await expect(page.locator(".query-built-code")).toHaveText(
    '"marché aux fleurs"',
  );
  for (const scheme of ["light", "dark"] as const) {
    await page.emulateMedia({ colorScheme: scheme });
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= window.innerWidth,
      ),
    ).toBe(true);
    await page.screenshot({
      path: info.outputPath(`alerts-mobile-${scheme}.png`),
      fullPage: true,
    });
  }
});
