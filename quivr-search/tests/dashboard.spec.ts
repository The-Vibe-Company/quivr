import { test, expect, type Page } from "@playwright/test";
import { fakeEngine, type Engine } from "./fake-engine";

// The monitoring dashboard's flows against a fake engine behind the facade
// routes (tests/fake-engine.ts): the real bundle and synthetic data, no core.
// The real-stack specs (feed, alerts, sources) cover the same pages end to end.

let engine: Engine;
test.beforeEach(async ({ page }) => {
  engine = await fakeEngine(page);
});
test.afterEach(async () => {
  await engine.close();
});

const rows = (page: Page) =>
  page.getByRole("list", { name: "Derniers éléments" }).locator(".row");
const row = (page: Page, title: string) =>
  rows(page).filter({ hasText: title });
const chips = (page: Page) => page.getByRole("group", { name: "Filtrer le fil" });
const side = (page: Page) =>
  page.getByRole("complementary", { name: "Alertes et sources" });
const sent = (path: string) =>
  engine.sent.filter((s) => s.path === path).map((s) => s.body);
// The body of the next POST to path. Start it before the click that sends it:
// the click returns before the browser has sent the request (THE-773).
const posted = (page: Page, path: string) =>
  page
    .waitForRequest(
      (r) => r.method() === "POST" && new URL(r.url()).pathname === path,
      { timeout: 10_000 },
    )
    .then((r) => r.postDataJSON());
const noOverflow = (page: Page) =>
  page.evaluate(() => document.documentElement.scrollWidth <= innerWidth);

test("le fil marque les non-lus, filtre par alerte et par source, et garde les arrivées en attente", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/");
  await expect(rows(page)).toHaveCount(8);
  await expect(page.locator(".bar-live")).toHaveText("En direct");
  // A first visit counts the last hour as unread.
  await chips(page).getByRole("button", { name: /^Non lus/ }).click();
  await expect(rows(page)).toHaveCount(5);
  await chips(page).getByRole("button", { name: /^Attrapés par une alerte/ }).click();
  await expect(rows(page)).toHaveCount(3);
  await chips(page).getByRole("button", { name: /^Tout/ }).click();

  // An alert card and a source row each filter the feed, and undo it.
  await side(page).getByRole("button", { name: /^Orages et grêle/ }).click();
  await expect(chips(page).getByRole("button", { name: /^Orages et grêle/ })).toBeVisible();
  await expect(rows(page)).toHaveCount(2);
  await side(page).getByRole("button", { name: /^Revue technique/ }).click();
  await expect(rows(page)).toHaveCount(2);
  await expect(row(page, "Batteries")).toBeVisible();
  await chips(page).getByRole("button", { name: /^Revue technique/ }).click();
  await expect(rows(page)).toHaveCount(8);

  // Reading an article marks it read, in this browser.
  await row(page, "Orages : la grêle").getByRole("link").click();
  await expect(row(page, "Orages : la grêle")).not.toHaveAttribute("data-unread");
  await expect(chips(page).getByRole("button", { name: /^Non lus/ })).toContainText("4");

  // While an article is open, an arrival waits behind the button.
  const first = engine.arrive();
  const pending = page.getByRole("button", { name: /1 nouvel article · Afficher/ });
  await expect(pending).toBeVisible();
  await expect(rows(page)).toHaveCount(8);
  await pending.click();
  await expect(rows(page).first()).toContainText(first.title);
  await expect(rows(page).first()).toHaveAttribute("data-unread", "true");

  // Paused, arrivals wait too; resuming shows the next ones at once.
  await page.keyboard.press("Escape");
  await page.getByRole("button", { name: "En direct" }).click();
  await expect(page.locator(".bar-live")).toHaveText("En pause");
  const second = engine.arrive();
  await expect(page.getByRole("button", { name: /1 nouvel article/ })).toBeVisible();
  await page.getByRole("button", { name: /1 nouvel article/ }).click();
  await expect(rows(page).first()).toContainText(second.title);

  await page.reload();
  await expect(row(page, "Orages : la grêle")).toBeVisible();
  await expect(row(page, "Orages : la grêle")).not.toHaveAttribute("data-unread");
});

test("la recherche passe des mots exacts aux idées proches et devient une alerte", async ({
  page,
}) => {
  await page.goto("/");
  const box = page.getByRole("searchbox", { name: "Rechercher dans le fil" });
  // With "Idées proches" on, a word no article uses still finds the subject.
  await box.fill("tempête");
  await expect(page.getByRole("heading", { name: /articles sur « tempête »/ })).toBeVisible();
  await expect(row(page, "Inondations").locator(".row-near")).toHaveText(
    "Même sujet, autres mots",
  );
  expect(engine.searches.at(-1)).toEqual({ query: "tempête", mode: "hybrid", limit: 50 });
  await page.getByRole("switch", { name: "Idées proches" }).click();
  await expect(page.getByRole("heading", { name: "Aucun article ne parle de ça." })).toBeVisible();
  expect(engine.searches.at(-1)?.mode).toBe("lexical");

  // Exact words, highlighted, most recent first on demand.
  await box.fill("grêle");
  await expect(page.getByRole("heading", { name: /3 articles sur « grêle »/ })).toBeVisible();
  await expect(rows(page).first().locator("mark").first()).toBeVisible();
  await page.getByRole("group", { name: "Trier" }).getByRole("button", { name: "Récents" }).click();
  await expect(rows(page).first()).toContainText("Orages : la grêle");

  await page.getByRole("button", { name: "Créer une alerte" }).click();
  await expect(page.getByRole("button", { name: "Alerte créée ✓" })).toBeVisible();
  await expect(page.locator(".toast")).toContainText("« grêle »");
  expect(sent("/demo/alerts").at(-1)).toMatchObject({
    name: "grêle",
    expression: { kind: "keywords", match: { term: "grêle" } },
  });

  // Escape clears the search when nothing is open.
  await box.blur();
  await page.keyboard.press("Escape");
  await expect(box).toHaveValue("");
  await expect(page.getByRole("heading", { name: "Aujourd’hui" })).toBeVisible();
});

test("le lecteur dit pourquoi l’article est attrapé, propose le même sujet et se pilote au clavier", async ({
  page,
}) => {
  await page.goto("/");
  await row(page, "Les conducteurs de tramway").getByRole("link").click();
  const reader = page.getByRole("complementary", { name: /Les conducteurs de tramway/ });
  await expect(reader.getByRole("list", { name: "Pourquoi cet article" })).toContainText(
    "Attrapé par votre alerte « Grèves dans les transports » — Quivr a jugé qu’il correspond à votre description (confiance 91 %).",
  );
  // A corrected article says what changed, word by word, and keeps the
  // earlier text readable.
  const updated = reader.locator(".reader-updated");
  await expect(updated).toContainText("Article corrigé il y a 4 min");
  await expect(updated).toContainText("9 mots retirés, 10 mots ajoutés.");
  await updated.getByRole("button", { name: "Voir les changements" }).click();
  const titleDiff = updated.getByLabel("Changements du titre");
  await expect(titleDiff.locator("del")).toHaveText(["menacent de cesser "]);
  await expect(titleDiff.locator("ins")).toHaveText(["cessent "]);
  await expect(updated.getByLabel("Changements du texte").locator("del")).toContainText(["demain."]);
  await updated.getByRole("button", { name: "Lire la version précédente" }).click();
  await expect(reader.getByRole("heading", { level: 2 })).toHaveText("Les conducteurs de tramway menacent de cesser le travail");
  await expect(reader.getByTestId("canonical-text")).toContainText("Les lignes restent ouvertes.");
  await updated.getByRole("button", { name: "Revenir à la version actuelle" }).click();
  await expect(reader.getByTestId("canonical-text")).toContainText("jusqu’à nouvel ordre");
  const original = reader.getByRole("link", { name: "Ouvrir l’original" });
  await expect(original).toHaveAttribute("href", "https://news.example.org/tram");
  await expect(original).toHaveAttribute("target", "_blank");

  // ↑ ↓ move through the feed; the storm article says which words matched
  // and lists articles on the same subject from a semantic search.
  await page.keyboard.press("ArrowUp");
  await expect(page.getByRole("heading", { name: "Compte rendu de la réunion du matin", level: 2 })).toBeVisible();
  await expect(page.getByRole("link", { name: "Ouvrir l’original" })).toHaveCount(0);
  await page.keyboard.press("ArrowUp");
  await page.keyboard.press("ArrowUp");
  const storm = page.getByRole("complementary", { name: /Orages : la grêle/ });
  await expect(storm.getByRole("list", { name: "Pourquoi cet article" })).toContainText(
    "Il contient « orage », « grêle ».",
  );
  await expect(storm.getByTestId("canonical-text").locator("mark")).toHaveText(["orageux"]);
  const near = storm.getByRole("region", { name: "Sur le même sujet" });
  await expect(near.getByRole("button")).toHaveCount(3);
  expect(engine.searches.some((s) => s.mode === "semantic")).toBe(true);
  await near.getByRole("button", { name: /Cellule orageuse/ }).click();
  await expect(page.getByRole("heading", { name: /Cellule orageuse/, level: 2 })).toBeVisible();

  // Escape closes the reader and puts the focus back on the article's row.
  await page.keyboard.press("Escape");
  await expect(page.getByRole("complementary", { name: "Alertes et sources" })).toBeVisible();
  await expect(row(page, "Cellule orageuse").getByRole("link")).toBeFocused();

  await row(page, "Batteries").getByRole("link").click();
  await page.getByRole("button", { name: "Chercher le même sujet" }).click();
  await expect(page.getByRole("searchbox")).toHaveValue("Batteries : une usine pilote annoncée");
  await expect(page.getByRole("switch", { name: "Idées proches" })).toHaveAttribute("aria-checked", "true");
});

test("le formulaire d’alerte compose mots, exclusions et sources, et garde la requête avancée", async ({
  page,
}) => {
  await page.goto("/?view=alerts");
  const form = page.getByRole("form", { name: "Nouvelle alerte" });
  const words = form.getByLabel("Mots à surveiller");
  await words.fill("orage");
  await words.press("Enter");
  await words.fill("vent");
  await words.press("Enter");
  await expect(form.getByRole("list", { name: "Mots surveillés" }).getByRole("listitem")).toHaveText(["orage×", "vent×"]);
  await form.getByRole("group", { name: "Combinaison des mots" }).getByRole("button", { name: "Tous ces mots" }).click();
  await form.getByLabel(/Ignorer les articles/).fill("football, publicité");
  await form.getByRole("group", { name: "Sources surveillées" }).getByRole("button", { name: "Météo locale" }).click();
  await expect(form.locator(".form-preview .keyword")).toHaveText([
    "orage", "vent", "source = Météo locale", "football", "publicité",
  ]);
  await form.getByLabel(/Nom de l’alerte/).fill("Vent et orage");
  await form.getByRole("button", { name: "Créer l’alerte" }).click();
  await expect(page.locator(".toast")).toContainText("« Vent et orage »");
  expect(sent("/demo/alerts").at(-1)).toMatchObject({
    name: "Vent et orage",
    expression: {
      kind: "keywords",
      match: {
        all: [
          { term: "orage" },
          { term: "vent" },
          { field: "source", equals: "Météo locale" },
          { not: { term: "football" } },
          { not: { term: "publicité" } },
        ],
      },
    },
  });
  const list = page.getByRole("list", { name: "Alertes" });
  await expect(list.getByRole("listitem").first()).toContainText(
    "Parle de « orage » et « vent » — sauf « football », « publicité » · Météo locale",
  );

  // An alert the chips cannot show opens in the advanced query; its name stays.
  const ports = list.getByRole("listitem").filter({ hasText: "Ports et quais" });
  await ports.getByRole("button", { name: "Modifier" }).click();
  const edit = page.getByRole("form", { name: "Modifier l’alerte" });
  await expect(edit.getByLabel("Requête avancée")).toHaveValue(
    "(port OR quai) AND grève AND NOT (football OR rugby)",
  );
  await expect(edit.getByLabel(/Nom de l’alerte/)).toHaveJSProperty("readOnly", true);
  await edit.getByLabel("Requête avancée").fill("(port OR quai) AND grève");
  const saved = posted(page, "/demo/alerts/alert_port/edit");
  await edit.getByRole("button", { name: "Enregistrer" }).click();
  expect((await saved).expression).toEqual({
    kind: "keywords",
    match: { all: [{ any: [{ term: "port" }, { term: "quai" }] }, { term: "grève" }] },
  });

  // A simple keyword alert comes back as chips.
  const storm = list.getByRole("listitem").filter({ hasText: "Orages et grêle" });
  await storm.getByRole("button", { name: "Modifier" }).click();
  await expect(
    page.getByRole("form", { name: "Modifier l’alerte" }).getByRole("list", { name: "Mots surveillés" }).getByRole("listitem"),
  ).toHaveText(["orage×", "grêle×"]);
  await expect(page.getByLabel(/Ignorer les articles/)).toHaveValue("football");
  await page.getByRole("button", { name: "Annuler" }).click();

  // Described alerts: a sentence, every source.
  await page.getByRole("group", { name: "Type d’alerte" }).getByRole("button", { name: "Un sujet décrit" }).click();
  await expect(page.getByLabel("Décrivez le sujet en une phrase")).toBeVisible();
  await expect(page.getByRole("group", { name: "Sources surveillées" })).toHaveCount(0);
  await expect(page.getByText("Une alerte décrite surveille toutes les sources.")).toBeVisible();

  // Pause, then delete after a confirmation.
  await storm.getByRole("button", { name: "Mettre en pause" }).click();
  await expect(storm.locator(".alert-state")).toHaveText("En pause");
  await storm.getByRole("button", { name: "Supprimer" }).click();
  await storm.getByRole("group", { name: "Supprimer Orages et grêle" }).getByRole("button", { name: "Oui, supprimer" }).click();
  await expect(list.getByRole("listitem").filter({ hasText: "Orages et grêle" })).toHaveCount(0);
});

test("l’aperçu d’une alerte montre les derniers articles qu’elle aurait attrapés, sans rien enregistrer", async ({
  page,
}) => {
  await page.goto("/?view=alerts");
  const form = page.getByRole("form", { name: "Nouvelle alerte" });
  const preview = form.locator(".alert-preview");
  const words = form.getByLabel("Mots à surveiller");
  await words.fill("grêle");
  await words.press("Enter");
  await expect(preview.locator(".alert-preview-title")).toHaveText(
    "Sur les 8 derniers articles, 3 auraient été attrapés.",
  );
  await expect(preview.getByRole("listitem")).toHaveCount(3);
  // The words to ignore are part of the previewed alert.
  const excluded = posted(page, "/demo/alerts/preview");
  await form.getByLabel(/Ignorer les articles/).fill("football");
  expect((await excluded).expression).toEqual({
    kind: "keywords",
    match: { all: [{ term: "grêle" }, { not: { term: "football" } }] },
  });
  await expect(preview.locator(".alert-preview-title")).toHaveText(
    "Sur les 8 derniers articles, 2 auraient été attrapés.",
  );

  // A described alert costs a classifier call per article: only on request.
  await form.getByRole("group", { name: "Type d’alerte" }).getByRole("button", { name: "Un sujet décrit" }).click();
  await form.getByLabel("Décrivez le sujet en une phrase").fill("Une grève des transports");
  const before = sent("/demo/alerts/preview").length;
  const asked = posted(page, "/demo/alerts/preview");
  await form.getByRole("button", { name: "Tester sur les derniers articles" }).click();
  expect((await asked).expression).toEqual({ kind: "described", description: "Une grève des transports" });
  await expect(preview.locator(".alert-preview-title")).toHaveText(
    "Sur les 8 derniers articles, 1 aurait été attrapé.",
  );
  await expect(preview.getByRole("listitem")).toHaveText([/Les conducteurs de tramway cessent le travail/]);
  expect(sent("/demo/alerts/preview").length).toBe(before + 1);
  expect(sent("/demo/alerts")).toEqual([]);
});

test("Sources : l’adresse d’un site trouve son fil, une adresse privée est refusée", async ({
  page,
}) => {
  await page.goto("/?view=sources");
  const address = page.getByLabel("Adresse du site");
  // Looked up as soon as typing pauses.
  await address.fill("www.example.org");
  await expect(page.getByText("Fil trouvé")).toBeVisible();
  await expect(page.getByLabel("Nom de la source")).toHaveValue("www.example.org — À la une");
  await page.getByRole("group", { name: "Vérifier les nouveautés toutes les…" }).getByRole("button", { name: "1 h" }).click();
  const created = posted(page, "/v0/connectors");
  await page.getByRole("button", { name: "Commencer la collecte" }).click();
  expect(await created).toMatchObject({
    kind: "rss",
    source_namespace: "www.example.org — À la une",
    config: { url: "https://www.example.org/rss.xml" },
    schedule: { interval_seconds: 3600 },
  });
  const list = page.getByRole("list", { name: "Sources" });
  await expect(list.getByRole("button", { name: "www.example.org — À la une", exact: true })).toBeVisible();
  await expect(address).toHaveValue("");

  await address.fill("http://10.0.0.1/feed.xml");
  await address.press("Enter");
  await expect(page.getByRole("alert")).toContainText("réseau privé ou local");
  await expect(page.getByRole("alert")).toBeFocused();
  await expect(page.getByRole("button", { name: "Commencer la collecte" })).toBeDisabled();

  await page.getByRole("button", { name: "Ajouter Fil exemple — International" }).click();
  await expect(page.getByRole("button", { name: "Fil exemple — International (déjà suivie)" })).toBeDisabled();

  // A failing source says so in plain words; pause, resume and remove.
  const weather = list.getByRole("listitem").filter({ hasText: "Météo locale" });
  await expect(weather.locator('[data-state="failing"]')).toHaveText("Ne répond plus");
  await expect(weather).toContainText("Le site ne répond pas.");
  await expect(page.locator(".list-count")).toHaveText("1 à vérifier");
  // Checking it again now reports the source answering once that check ends.
  await weather.getByRole("button", { name: "Réessayer Météo locale" }).click();
  await expect(weather.locator('[data-state="active"]')).toBeVisible();
  await expect(weather.getByRole("button", { name: "Réessayer Météo locale" })).toHaveCount(0);
  await weather.getByRole("button", { name: "Mettre en pause Météo locale" }).click();
  await expect(weather.locator('[data-state="paused"]')).toBeVisible();
  await weather.getByRole("button", { name: "Reprendre Météo locale" }).click();
  await expect(weather.locator('[data-state="paused"]')).toHaveCount(0);
  await weather.getByRole("button", { name: "Retirer Météo locale" }).click();
  await weather.getByRole("group", { name: "Retirer Météo locale" }).getByRole("button", { name: "Oui, retirer" }).click();
  await expect(list.getByRole("button", { name: "Météo locale", exact: true })).toHaveCount(0);
  await expect(page.getByRole("heading", { name: "Vos sources" })).toBeFocused();
});

test("le tableau de bord tient sur un téléphone, en clair et en sombre", async ({
  page,
}, info) => {
  for (const [name, size] of [
    ["desktop", { width: 1440, height: 900 }],
    ["mobile", { width: 390, height: 844 }],
  ] as const) {
    await page.setViewportSize(size);
    await page.goto("/");
    await expect(rows(page)).toHaveCount(8);
    await page.screenshot({ path: info.outputPath(`${name}-feed.png`) });
    await row(page, "Orages : la grêle").getByRole("link").click();
    await expect(page.getByTestId("canonical-text")).toBeVisible();
    await page.screenshot({ path: info.outputPath(`${name}-reader.png`) });
    await page.keyboard.press("Escape");
    await page.getByRole("searchbox", { name: "Rechercher dans le fil" }).fill("orage");
    await expect(page.getByRole("heading", { name: /articles sur « orage »/ })).toBeVisible();
    await page.screenshot({ path: info.outputPath(`${name}-search.png`) });
    for (const [tab, heading] of [["Alertes", "Nouvelle alerte"], ["Sources", "Vos sources"]]) {
      await page.getByRole("navigation", { name: "Sections" }).getByRole("link", { name: new RegExp(`^${tab}`) }).click();
      await expect(page.getByRole("heading", { name: heading })).toBeVisible();
      for (const scheme of ["light", "dark"] as const) {
        await page.emulateMedia({ colorScheme: scheme });
        expect(await noOverflow(page), `${name} ${tab} ${scheme}`).toBe(true);
      }
      await page.emulateMedia({ colorScheme: "light" });
      await page.screenshot({
        path: info.outputPath(`${name}-${tab.toLowerCase()}.png`),
        fullPage: name === "mobile",
      });
    }
  }
});
