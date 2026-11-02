import { test } from '@e2e-dev/web';
import { expect } from 'e2e';
import { WRITES_ONLY_LOCALLY, runWord, writable } from '../support/target.ts';

// Journeys 5 and 6 of Spec 26. Discovery, intervals, errors and health rules
// are owned by quivr-search/tests/sources.spec.ts and connectors.spec.ts.
test('the Sources tab lists the demo’s sources', { session: 'demo', tags: ['live'] }, async ({ app, screen }) => {
  await app.open('/?view=sources');
  await expect(screen.getByRole('heading', { name: 'Sources', level: 1 })).toBeVisible({ timeout: 15_000 });
  await expect(screen.getByRole('list', 'Sources').getByRole('listitem').first()).toBeVisible({ timeout: 15_000 });
  await app.screenshot('sources');
});

test('a source from the local test site is added, then removed', { session: 'demo', tags: ['local'] }, async ({ app, screen }) => {
  const feeds = process.env.QUIVR_DEMO_FEEDS_URL;
  test.skip(!writable(app.baseUrl) || !feeds, `${WRITES_ONLY_LOCALLY}, with QUIVR_DEMO_FEEDS_URL`);
  const name = `Journal ${runWord()}`;
  // The test site numbers its synthetic sources; one of its own keeps this source apart.
  const site = 100 + Math.floor(Math.random() * 900);
  await app.open('/?view=sources');
  await screen.getByRole('button', 'Ajouter une source').tap();
  const address = screen.getByLabel('Adresse du site');
  await address.fill(`${feeds}/feeds/bulk.xml?source=${site}&items=3`);
  await address.press('Enter');
  const form = screen.getByRole('form', 'Ajouter une source');
  await expect(form.getByText('Fil trouvé')).toBeVisible({ timeout: 15_000 });
  await form.getByLabel('Nom de la source').fill(name);
  await form.getByRole('button', 'Commencer la collecte').tap();

  const card = screen.getByRole('list', 'Sources').getByRole('listitem').filter({ hasText: name });
  await expect(card).toBeVisible({ timeout: 15_000 });
  await card.getByRole('button', `Plus d’actions pour ${name}`).tap();
  await card.getByRole('menuitem', 'Retirer').tap();
  await card.getByRole('group', `Retirer ${name}`).getByRole('button', 'Oui, retirer').tap();
  await expect(card).toHaveCount(0);
});
