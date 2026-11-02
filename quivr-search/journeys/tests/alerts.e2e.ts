import { test } from '@e2e-dev/web';
import { expect } from 'e2e';
import { headlineWord } from '../support/headline.ts';
import { WRITES_ONLY_LOCALLY, writable } from '../support/target.ts';

// Journeys 7 and 8 of Spec 26. The preview saves nothing on the server, so it
// runs on the live demo: it fails when the core refuses the demo's alerts
// evaluator, as on 2026-11-02 (THE-2150). The query builder, described
// alerts and what an alert catches are owned by quivr-search/tests/alerts.spec.ts.
test('a keyword alert previews what it would have caught, and Annuler saves nothing', { session: 'demo', tags: ['live'] }, async ({ app, screen, browser }) => {
  await app.open('/');
  const word = await headlineWord(screen);
  await app.open('/?view=alerts');
  await screen.getByRole('button', 'Nouvelle alerte').tap();
  await screen.getByLabel('Tous ces mots').fill(word);
  await expect(browser.locator('.alert-preview-title')).toHaveText(/sur les \d+ derniers$/, { timeout: 20_000 });
  await app.screenshot('alert-preview');
  await screen.getByRole('button', 'Annuler').tap();
  await expect(screen.getByLabel('Tous ces mots')).toBeHidden();
});

test('a keyword alert is saved, paused and deleted', { session: 'demo', tags: ['local'] }, async ({ app, screen }) => {
  test.skip(!writable(app.baseUrl), WRITES_ONLY_LOCALLY);
  const name = `Alerte ${Math.random().toString(36).slice(2, 8)}`;
  await app.open('/?view=alerts');
  await screen.getByRole('button', 'Nouvelle alerte').tap();
  await screen.getByLabel('Tous ces mots').fill('orage');
  await screen.getByLabel('Nom de l’alerte').fill(name);
  await screen.getByRole('button', 'Créer l’alerte').tap();

  const row = screen.getByRole('table', 'Alertes').getByRole('row').filter({ hasText: name });
  await expect(row).toBeVisible({ timeout: 15_000 });
  await row.getByRole('button', name).tap();
  const actions = screen.getByRole('group', 'Actions de l’alerte');
  const active = actions.getByRole('switch', 'Alerte active');
  await active.tap();
  await expect(active).toHaveAttribute('aria-checked', 'false');
  await actions.getByRole('button', `Plus d’actions pour ${name}`).tap();
  await actions.getByRole('menuitem', 'Supprimer').tap();
  await screen.getByRole('group', 'Confirmer la suppression').getByRole('button', 'Supprimer définitivement').tap();
  await expect(screen.getByRole('heading', { name: 'Alertes', level: 1 })).toBeVisible();
  await expect(row).toHaveCount(0);
});
