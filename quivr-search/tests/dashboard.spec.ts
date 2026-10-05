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
const side = (page: Page) => page.getByRole("complementary", { name: "Tendances" });
// The panel of a filter menu of the feed's top bar, opened.
async function menu(page: Page, title: string) {
  const button = chips(page).getByRole("button", { name: new RegExp(`^${title}`) });
  if ((await button.getAttribute("aria-expanded")) !== "true") await button.click();
  return page.getByRole("dialog", { name: title });
}
// Ticks (or unticks) one option of a filter menu, then closes it.
async function pick(page: Page, title: string, option: RegExp) {
  await (await menu(page, title)).getByRole("button", { name: option }).click();
  await page.keyboard.press("Escape");
}
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
// The top bar's title side and action side each fit their place: neither
// spills over the search between them.
const barFits = (page: Page) =>
  page.evaluate(() =>
    [".bar-view", ".bar-actions"].every((selector) => {
      const part = document.querySelector(selector);
      if (!part) return true;
      const left = part.getBoundingClientRect().left;
      // Spilling right shows in its scroll width; spilling left (the actions
      // end at the right) shows in where its children start.
      return (
        part.scrollWidth <= part.clientWidth + 1 &&
        [...part.children].every((child) => child.getBoundingClientRect().left >= left - 1)
      );
    }),
  );

test("le fil marque les non-lus, filtre par alerte et par source, et retient les arrivées en pause", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/");
  await expect(rows(page)).toHaveCount(8);
  await expect(page.locator(".bar-live")).toHaveText("En direct");
  // A first visit counts the last hour as unread.
  await chips(page).getByRole("button", { name: /^Non lus/ }).click();
  await expect(rows(page)).toHaveCount(5);
  await pick(page, "Alertes", /^Toutes les alertes/);
  await expect(rows(page)).toHaveCount(3);
  await chips(page).getByRole("button", { name: "Tout effacer" }).click();
  await expect(rows(page)).toHaveCount(8);

  // Alerts and sources filter the feed, several of each at once.
  await pick(page, "Alertes", /^Orages et grêle/);
  await expect(rows(page)).toHaveCount(2);
  await pick(page, "Alertes", /^Orages et grêle/);
  await pick(page, "Sources", /^Revue technique/);
  await expect(rows(page)).toHaveCount(2);
  await expect(row(page, "Batteries")).toBeVisible();
  await pick(page, "Sources", /^Météo locale/);
  await expect(rows(page)).toHaveCount(3);
  await chips(page).getByRole("button", { name: "Tout effacer" }).click();
  await expect(rows(page)).toHaveCount(8);

  // The last hour folds away and comes back (the five articles under an hour old).
  const lastHour = page.getByRole("button", { name: /^Dernière heure/ });
  await lastHour.click();
  await expect(lastHour).toHaveAttribute("aria-expanded", "false");
  await expect(rows(page)).toHaveCount(3);
  await lastHour.click();
  await expect(rows(page)).toHaveCount(8);

  // An alert's tag on an article filters on that alert; a second click undoes it.
  const tag = () => row(page, "Orages : la grêle").getByRole("button", { name: /Orages et grêle/ });
  await tag().click();
  await expect(rows(page)).toHaveCount(2);
  await tag().click();
  await expect(rows(page)).toHaveCount(8);

  // A muted source leaves the feed until it is put back.
  const sources = await menu(page, "Sources");
  await sources.getByRole("button", { name: "Masquer Dépêches exemple du fil" }).click();
  await expect(rows(page)).toHaveCount(4);
  await sources.getByRole("button", { name: "Remettre Dépêches exemple dans le fil" }).click();
  await page.keyboard.press("Escape");
  await expect(rows(page)).toHaveCount(8);

  // A topic of the moment searches it.
  await side(page).getByRole("button", { name: /^Grêle/ }).click();
  await expect(page.getByRole("searchbox")).toHaveValue("grêle");
  await page.getByRole("searchbox").fill("");

  // Reading an article marks it read, in this browser.
  await row(page, "Orages : la grêle").getByRole("link").click();
  await expect(row(page, "Orages : la grêle")).not.toHaveAttribute("data-unread");
  // A second click on the open article closes it; a third opens it again.
  await row(page, "Orages : la grêle").getByRole("link").click();
  await expect(page.locator(".peek")).toHaveCount(0);
  await row(page, "Orages : la grêle").getByRole("link").click();
  await expect(page.locator(".peek")).toHaveCount(1);
  // A click beside it, outside the feed's rows, closes it too.
  await page.locator(".bar-view").click();
  await expect(page.locator(".peek")).toHaveCount(0);
  await row(page, "Orages : la grêle").getByRole("link").click();
  await expect(page.locator(".peek")).toHaveCount(1);

  // Even with an article open, an arrival goes straight to the top, unread.
  const first = engine.arrive();
  await expect(rows(page)).toHaveCount(9);
  await expect(rows(page).first()).toContainText(first.title);
  await expect(rows(page).first()).toHaveAttribute("data-unread", "true");

  // The reader closed, the filters are back and count what is left to read.
  await page.keyboard.press("Escape");
  await expect(chips(page).getByRole("button", { name: /^Non lus/ })).toContainText("5");

  // Paused, arrivals wait; resuming shows them at once.
  await page.getByRole("button", { name: "En direct" }).click();
  await expect(page.locator(".bar-live")).toHaveText("En pause");
  const second = engine.arrive();
  await expect(page.locator(".toast")).toContainText("à la reprise");
  await expect(rows(page)).toHaveCount(9);
  await page.getByRole("button", { name: "En pause" }).click();
  await expect(rows(page).first()).toContainText(second.title);

  await page.reload();
  await expect(row(page, "Orages : la grêle")).toBeVisible();
  await expect(row(page, "Orages : la grêle")).not.toHaveAttribute("data-unread");

  // The Alertes tab counts only the unread articles an alert caught, and
  // "Tout marquer comme lu" reads every article shown.
  const badge = page.getByRole("navigation", { name: "Sections" }).locator('.rail-badge[data-kind="alerts"]');
  await expect(badge).toContainText("2 articles non lus attrapés par vos alertes");
  await chips(page).getByRole("button", { name: "Tout marquer comme lu" }).click();
  await expect(chips(page).getByRole("button", { name: /^Non lus/ })).toContainText("0");
  await expect(badge).toHaveCount(0);
  await expect(chips(page).getByRole("button", { name: "Tout marquer comme lu" })).toHaveCount(0);
});

test("le filtre Date liste chaque jour jusqu’au premier article, compté par Quivr, et charge un jour ancien", async ({
  page,
}, info) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.goto("/");
  await expect(rows(page)).toHaveCount(8);
  // Tout counts what Quivr holds, not the articles the feed loaded.
  const total = engine.ws.articles.length + engine.ws.archive.length;
  const all = chips(page).getByRole("button", { name: /^Tout\b/ }).first();
  await expect(all.locator(".chip-count")).toHaveText(String(total));
  await expect(page.getByText(/Les articles plus anciens restent dans Quivr/)).toBeVisible();
  // An article arriving live after Quivr counted adds one, once.
  engine.arrive();
  await expect(rows(page)).toHaveCount(9);
  await expect(all.locator(".chip-count")).toHaveText(String(total + 1));

  // Every day back to the first article, five days ago, newest first.
  const label = (iso: string) => {
    const day = new Date(iso);
    const text = new Intl.DateTimeFormat("fr-FR", {
      weekday: "long",
      day: "numeric",
      month: "short",
      ...(day.getFullYear() === new Date().getFullYear() ? {} : { year: "numeric" }),
    }).format(day);
    return text[0].toUpperCase() + text.slice(1);
  };
  // Labels come from the fixture's own times, so a run across midnight holds.
  const [port, , , first] = engine.ws.archive;
  const portDay = new Date(port.received_at!);
  const dayBefore = new Date(portDay);
  dayBefore.setDate(dayBefore.getDate() - 1);
  const firstDay = new Date(first.received_at!).toDateString();
  const earlier: string[] = [];
  for (let n = 2; earlier.length < 30; n++) {
    const at = new Date();
    at.setHours(12, 0, 0, 0);
    at.setDate(at.getDate() - n);
    earlier.push(label(at.toISOString()));
    if (at.toDateString() === firstDay) break;
  }
  const dates = await menu(page, "Date");
  const options = dates.locator(".menu-option");
  await expect(options.locator(".menu-label")).toHaveText([
    "Tous les jours",
    "Aujourd’hui",
    "Hier",
    ...earlier,
  ]);
  await expect(options.first().locator(".menu-count")).toHaveText(String(total + 1));
  const old = options.filter({ hasText: label(port.received_at!) });
  await expect(old.locator(".menu-count")).toHaveText("3");
  await expect(options.filter({ hasText: label(dayBefore.toISOString()) }).locator(".menu-count")).toHaveText("0");
  await page.screenshot({ path: info.outputPath("date-menu-light.png") });

  // A day older than the feed loads from Quivr, newest first, page by page.
  await old.click();
  await page.keyboard.press("Escape");
  await expect(rows(page).locator(".row-title")).toHaveText([
    "Le port rouvre après trois jours de fermeture",
    "Un pont suspendu inspecté par drone",
    "Le marché couvert rouvre ses portes",
  ]);
  await expect(all.locator(".chip-count")).toHaveText("3");
  await expect(page.getByText(/Les articles plus anciens restent dans Quivr/)).toHaveCount(0);
  await expect(chips(page).getByRole("button", { name: /^Date/ })).toHaveAttribute(
    "aria-label",
    `Date : ${label(port.received_at!)}`,
  );
  // The source filter still applies to a day read from Quivr.
  await pick(page, "Sources", /^Revue technique/);
  await expect(rows(page)).toHaveCount(1);
  await expect(row(page, "Un pont suspendu")).toBeVisible();
  for (const scheme of ["light", "dark"] as const) {
    await page.emulateMedia({ colorScheme: scheme });
    await page.screenshot({ path: info.outputPath(`date-day-${scheme}.png`), animations: "disabled" });
  }
  await page.emulateMedia({ colorScheme: "light" });
  await chips(page).getByRole("button", { name: "Tout effacer" }).click();
  await expect(rows(page)).toHaveCount(9);

  await page.setViewportSize({ width: 390, height: 844 });
  await pick(page, "Date", new RegExp(`^${label(first.received_at!)}`));
  await expect(rows(page)).toHaveCount(1);
  await expect(row(page, "Les archives municipales")).toBeVisible();
  expect(await noOverflow(page)).toBe(true);
  await (await menu(page, "Date")).waitFor();
  await page.screenshot({ path: info.outputPath("date-menu-mobile.png"), animations: "disabled" });
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

  // A source picked while searching is searched on its own by the engine.
  await pick(page, "Sources", /^Météo locale/);
  await expect
    .poll(() => engine.searches.at(-1))
    .toEqual({ query: "grêle", mode: "lexical", limit: 50, sources: ["Météo locale"] });
  await expect(page.getByRole("heading", { name: /1 article sur « grêle »/ })).toBeVisible();
  await expect(rows(page).first()).toContainText("Cellule orageuse");
  // Several sources are searched together. A corpus the engine cannot filter
  // yet: the top 50 of every source, narrowed here.
  engine.options.sourceFilter = false;
  const before = engine.searches.length;
  await pick(page, "Sources", /^Dépêches exemple/);
  await expect.poll(() => engine.searches.slice(before)).toEqual([
    { query: "grêle", mode: "lexical", limit: 50, sources: ["Météo locale", "Dépêches exemple"] },
    { query: "grêle", mode: "lexical", limit: 50 },
  ]);
  await expect(page.getByRole("heading", { name: /3 articles sur « grêle »/ })).toBeVisible();
  await chips(page).getByRole("button", { name: "Tout effacer" }).click();

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
  await expect(page.getByRole("heading", { name: /sur « / })).toHaveCount(0);
  await expect(rows(page).first()).toBeVisible();
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
  await expect(side(page)).toBeVisible();
  await expect(row(page, "Cellule orageuse").getByRole("link")).toBeFocused();

  await row(page, "Batteries").getByRole("link").click();
  await page.getByRole("button", { name: "Creuser le sujet" }).click();
  await expect(page.getByRole("searchbox")).toHaveValue("Batteries : une usine pilote annoncée");
  await expect(page.getByRole("switch", { name: "Idées proches" })).toHaveAttribute("aria-checked", "true");
});

test("le formulaire d’alerte compose mots, exclusions et sources, et garde la requête avancée", async ({
  page,
}) => {
  await page.goto("/?view=alerts");
  // The form opens in a panel from the page's button.
  await page.getByRole("button", { name: "Nouvelle alerte" }).click();
  const form = page.getByRole("form", { name: "Nouvelle alerte" });
  const words = form.getByLabel("Mots à surveiller");
  await words.fill("orage");
  await words.press("Enter");
  await words.fill("vent");
  await words.press("Enter");
  await expect(form.getByRole("list", { name: "Mots surveillés" }).getByRole("listitem")).toHaveText(["orage×", "vent×"]);
  await form.getByRole("group", { name: "Combinaison des mots" }).getByRole("button", { name: "Tous ces mots" }).click();
  await form.getByLabel(/Ignorer les articles/).fill("football, publicité");
  // Sources are picked in the feed's source menu.
  await form.getByRole("button", { name: "Sources surveillées : Toutes les sources" }).click();
  await form.getByRole("dialog", { name: "Sources surveillées" }).getByRole("button", { name: "Météo locale" }).click();
  await page.keyboard.press("Escape");
  await expect(form.getByRole("button", { name: "Sources surveillées : Météo locale" })).toBeVisible();
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
  const table = page.getByRole("table", { name: "Alertes" });
  const rowOf = (name: string) => table.getByRole("row").filter({ hasText: name });
  const sheet = page.locator(".alert-sheet");
  const actions = sheet.getByRole("group", { name: "Actions de l’alerte" });
  // Saving closes the panel and selects the new alert: its sheet shows the rule.
  await expect(form).toHaveCount(0);
  await expect(sheet.getByRole("heading", { level: 2 })).toHaveText("Vent et orage");
  await expect(sheet.locator(".sheet-rule .kw")).toHaveText(["orage", "vent", "football", "publicité"]);
  await expect(sheet.locator(".sheet-rule .kw[data-not]")).toHaveText(["football", "publicité"]);
  await expect(sheet.locator(".rule-sources").getByRole("img", { name: "Météo locale" })).toBeVisible();

  // An alert the chips cannot show opens in the advanced query; it can be renamed.
  await rowOf("Ports et quais").getByRole("button", { name: "Ports et quais" }).click();
  await expect(sheet.locator(".rule-created")).toHaveCount(0);
  await actions.getByRole("button", { name: "Modifier" }).click();
  const edit = page.getByRole("form", { name: "Modifier l’alerte" });
  await expect(edit.getByLabel("Requête avancée")).toHaveValue(
    "(port OR quai) AND grève AND NOT (football OR rugby)",
  );
  await expect(edit.getByLabel(/Nom de l’alerte/)).toHaveValue("Ports et quais");
  await edit.getByLabel(/Nom de l’alerte/).fill("Grèves sur les quais");
  await edit.getByLabel("Requête avancée").fill("(port OR quai) AND grève");
  const saved = posted(page, "/demo/alerts/alert_port/edit");
  await edit.getByRole("button", { name: "Enregistrer" }).click();
  const body = await saved;
  expect(body.expression).toEqual({
    kind: "keywords",
    match: { all: [{ any: [{ term: "port" }, { term: "quai" }] }, { term: "grève" }] },
  });
  expect(body.name).toBe("Grèves sur les quais");
  await expect(rowOf("Grèves sur les quais")).toHaveCount(1);
  await expect(rowOf("Ports et quais")).toHaveCount(0);

  // The sheet's numbers: everything it caught, the last seven days against
  // the seven before, how often, and when it was created (noted by the facade).
  const storm = rowOf("Orages et grêle");
  await storm.getByRole("button", { name: "Orages et grêle" }).click();
  // The feed only reaches a few hours back: no growth is claimed, and the
  // week says how far it goes.
  await expect(sheet.locator(".kpi dd")).toHaveText(["2", "2", "0,1", "25 %"]);
  await expect(sheet.locator(".kpi dt").nth(1)).toHaveText(/^depuis \d h$/);
  await expect(sheet.locator(".insight")).toHaveCount(0);
  await expect(sheet.locator(".rule-created")).toHaveText(/^créée le \d{1,2} \S+$/);

  // A simple keyword alert comes back as chips.
  await actions.getByRole("button", { name: "Modifier" }).click();
  await expect(
    page.getByRole("form", { name: "Modifier l’alerte" }).getByRole("list", { name: "Mots surveillés" }).getByRole("listitem"),
  ).toHaveText(["orage×", "grêle×"]);
  await expect(
    page.getByRole("form", { name: "Modifier l’alerte" }).getByRole("list", { name: "Mots ignorés" }).getByRole("listitem"),
  ).toHaveText(["football×"]);
  await page.getByRole("button", { name: "Annuler" }).click();

  // Described alerts: a sentence, limited to the chosen sources.
  await page.getByRole("button", { name: "Nouvelle alerte" }).click();
  await page.getByRole("group", { name: "Type d’alerte" }).getByRole("button", { name: "Un sujet décrit" }).click();
  await page.getByLabel("Décrivez le sujet en une phrase").fill("Les grèves dans les transports publics");
  await page.getByRole("button", { name: /^Sources surveillées/ }).click();
  await page.getByRole("dialog", { name: "Sources surveillées" }).getByRole("button", { name: "Revue technique" }).click();
  await page.keyboard.press("Escape");
  await page.getByLabel(/Nom de l’alerte/).fill("Grèves, revue technique");
  const described = posted(page, "/demo/alerts");
  await page.getByRole("button", { name: "Créer l’alerte" }).click();
  expect((await described).expression).toEqual({
    kind: "described",
    description: "Les grèves dans les transports publics",
    sources: ["Revue technique"],
  });
  await expect(sheet.locator(".rule-quote")).toHaveText("« Les grèves dans les transports publics »");
  await expect(sheet.locator(".rule-sources").getByRole("img", { name: "Revue technique" })).toBeVisible();

  // Pause, then delete after a confirmation.
  await storm.getByRole("button", { name: "Orages et grêle" }).click();
  await actions.getByRole("switch", { name: "Alerte active" }).click();
  await expect(storm.locator(".alert-state")).toHaveText("En pause");
  await actions.getByRole("button", { name: "Plus d’actions pour Orages et grêle" }).click();
  await actions.getByRole("menuitem", { name: "Supprimer" }).click();
  await sheet
    .getByRole("group", { name: "Confirmer la suppression" })
    .getByRole("button", { name: "Supprimer définitivement" })
    .click();
  await expect(rowOf("Orages et grêle")).toHaveCount(0);
});

test("l’aperçu d’une alerte montre les derniers articles qu’elle aurait attrapés, sans rien enregistrer", async ({
  page,
}) => {
  await page.goto("/?view=alerts");
  await page.getByRole("button", { name: "Nouvelle alerte" }).click();
  const form = page.getByRole("form", { name: "Nouvelle alerte" });
  const preview = form.locator(".alert-preview");
  const words = form.getByLabel("Mots à surveiller");
  await words.fill("grêle");
  await words.press("Enter");
  await expect(preview.locator(".alert-preview-title")).toHaveText(
    "3 articles sur les 8 derniers",
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
    "2 articles sur les 8 derniers",
  );

  // A described alert costs a classifier call per article: only on request.
  await form.getByRole("group", { name: "Type d’alerte" }).getByRole("button", { name: "Un sujet décrit" }).click();
  await form.getByLabel("Décrivez le sujet en une phrase").fill("Une grève des transports");
  const before = sent("/demo/alerts/preview").length;
  const asked = posted(page, "/demo/alerts/preview");
  await form.getByRole("button", { name: "Tester sur les derniers articles" }).click();
  expect((await asked).expression).toEqual({ kind: "described", description: "Une grève des transports" });
  await expect(preview.locator(".alert-preview-title")).toHaveText(
    "1 article sur les 8 derniers",
  );
  await expect(preview.getByRole("listitem")).toHaveText([/Les conducteurs de tramway cessent le travail/]);
  expect(sent("/demo/alerts/preview").length).toBe(before + 1);
  expect(sent("/demo/alerts")).toEqual([]);
});

test("Sources : l’adresse d’un site trouve son fil, une adresse privée est refusée", async ({
  page,
}) => {
  await page.goto("/?view=sources");
  // Adding opens a dialog, from the header's button or the "+" card.
  // The page's count and its main action sit in the top bar.
  const banner = page.getByRole("banner");
  await expect(banner.locator(".head-count")).toHaveText("3 sources");
  await expect(banner.getByRole("button", { name: "Ajouter une source", exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Ajouter une source", exact: true }).click();
  const adding = page.getByRole("dialog", { name: "Ajouter une source" });
  const address = adding.getByLabel("Adresse du site");
  await expect(address).toBeFocused();
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
  // Added: the dialog closes on the new card, which takes the focus.
  await expect(adding).toHaveCount(0);
  const list = page.getByRole("list", { name: "Sources" });
  await expect(list.getByRole("button", { name: "www.example.org — À la une", exact: true })).toBeFocused();

  await page.getByRole("button", { name: /^Ajouter une source Un site/ }).click();
  await expect(address).toBeFocused();
  await address.fill("http://10.0.0.1/feed.xml");
  await address.press("Enter");
  await expect(page.getByRole("alert")).toContainText("réseau privé ou local");
  await expect(page.getByRole("alert")).toBeFocused();
  await expect(page.getByRole("button", { name: "Commencer la collecte" })).toBeDisabled();

  await page.getByRole("button", { name: "Ajouter Fil exemple — International" }).click();
  await expect(adding).toHaveCount(0);
  await page.getByRole("button", { name: "Ajouter une source", exact: true }).click();
  await expect(page.getByRole("button", { name: "Fil exemple — International (déjà suivie)" })).toBeDisabled();
  await page.keyboard.press("Escape");
  await expect(adding).toHaveCount(0);

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

  // Renamed in place, the source keeps its name in the feed's menus too.
  const wire = list.locator('[data-connector="con_wire"]');
  await wire.getByRole("button", { name: "Renommer Dépêches exemple" }).click();
  const field = wire.getByLabel("Nouveau nom de Dépêches exemple");
  await field.fill("Le fil des dépêches");
  const renamed = posted(page, "/demo/sources/rename");
  await field.press("Enter");
  expect(await renamed).toMatchObject({ name: "Le fil des dépêches" });
  await expect(wire.getByRole("button", { name: "Le fil des dépêches", exact: true })).toBeVisible();
  await expect(page.locator(".toast")).toContainText("s’appelle maintenant « Le fil des dépêches »");

  // The settings change the name and how often it is read, saved together.
  await wire.getByRole("button", { name: "Plus d’actions pour Le fil des dépêches" }).click();
  await wire.getByRole("menuitem", { name: "Réglages" }).click();
  const settings = page.getByRole("dialog", { name: "Réglages de Le fil des dépêches" });
  await expect(settings.getByRole("heading", { name: "Le fil des dépêches", level: 1 })).toBeVisible();
  await expect(settings.getByRole("link", { name: "https://news.example.org/feed.xml" })).toBeVisible();
  // A chart's bar says what it counts, drawn inside the modal (not under it).
  await settings.locator(".sc-spark [data-tip]").last().hover();
  await expect(settings.locator(".chart-tip")).toHaveText(/ · \d+ articles?$/);
  // From the keyboard, the chart reads today first, then the day before on ←.
  await settings.getByLabel("Nom").focus();
  await page.keyboard.press("Shift+Tab");
  await expect(settings.locator(".chart-tip")).toHaveText(/^Aujourd’hui · /);
  await page.keyboard.press("ArrowLeft");
  await expect(settings.locator(".chart-tip")).toHaveText(/^Hier · /);
  await settings.getByLabel("Nom").fill("Dépêches");
  await settings.getByRole("group", { name: "Vérifier les nouveautés toutes les…" }).getByRole("button", { name: "1 h" }).click();
  const scheduled = page
    .waitForRequest((r) => r.method() === "PUT" && new URL(r.url()).pathname === "/v0/connectors/con_wire/schedule")
    .then((r) => r.postDataJSON());
  await settings.getByRole("button", { name: "Enregistrer" }).click();
  expect(await scheduled).toEqual({ interval_seconds: 3600 });
  await expect(settings).toHaveCount(0);
  await expect(wire.getByRole("button", { name: "Dépêches", exact: true })).toBeVisible();
  await expect(wire.locator(".source-state")).toHaveAttribute("title", "Vérifiée toutes les 1 h");

  // Removed from its settings: they close and the focus lands on the list.
  await weather.getByRole("button", { name: "Météo locale", exact: true }).click();
  const weatherSettings = page.getByRole("dialog", { name: "Réglages de Météo locale" });
  await weatherSettings.getByRole("button", { name: "Retirer" }).click();
  await weatherSettings.getByRole("group", { name: "Retirer Météo locale" }).getByRole("button", { name: "Oui, retirer" }).click();
  await expect(weatherSettings).toHaveCount(0);
  await expect(list.getByRole("button", { name: "Météo locale", exact: true })).toHaveCount(0);
  await expect(page.getByRole("heading", { name: "Vos sources" })).toBeFocused();
});

test("revenir sur Alertes ou Sources les montre aussitôt, le temps de les relire", async ({
  page,
}) => {
  const nav = page.getByRole("navigation", { name: "Sections" });
  const alertsShown = page.getByRole("table");
  const sourcesShown = page.getByRole("list", { name: "Sources" });
  await page.goto("/?view=alerts");
  await expect(alertsShown).toBeVisible();
  await nav.getByRole("link", { name: /^Sources/ }).click();
  await expect(sourcesShown).toBeVisible();
  // From now on, both lists answer only once released: coming back, each
  // page is already there, not a loading state between two pages.
  let release!: () => void;
  const held = new Promise<void>((resolve) => (release = resolve));
  await page.route(
    (url) => ["/demo/alerts", "/v0/connectors"].includes(url.pathname),
    async (route) => {
      await held;
      await route.fallback();
    },
  );
  try {
    await nav.getByRole("link", { name: /^Alertes/ }).click();
    await expect(alertsShown).toBeVisible();
    await nav.getByRole("link", { name: /^Sources/ }).click();
    await expect(sourcesShown).toBeVisible();
  } finally {
    release();
  }
});

test("le tableau de bord tient sur un téléphone, en clair et en sombre", async ({
  page,
}, info) => {
  for (const [name, size] of [
    ["desktop", { width: 1440, height: 900 }],
    // Just above the phone layout: the tightest top bar.
    ["tablet", { width: 770, height: 900 }],
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
    for (const [tab, action] of [["Alertes", "Nouvelle alerte"], ["Sources", "Ajouter une source"]]) {
      await page.getByRole("navigation", { name: "Sections" }).getByRole("link", { name: new RegExp(`^${tab}`) }).click();
      await expect(page.getByRole("banner").getByRole("button", { name: action, exact: true })).toBeVisible();
      for (const scheme of ["light", "dark"] as const) {
        await page.emulateMedia({ colorScheme: scheme });
        expect(await noOverflow(page), `${name} ${tab} ${scheme}`).toBe(true);
        expect(await barFits(page), `${name} ${tab} ${scheme}: top bar`).toBe(true);
      }
      await page.emulateMedia({ colorScheme: "light" });
      await page.screenshot({
        path: info.outputPath(`${name}-${tab.toLowerCase()}.png`),
        fullPage: name === "mobile",
      });
    }
  }
});
