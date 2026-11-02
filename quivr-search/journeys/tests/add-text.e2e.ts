import { test } from '@e2e-dev/web';
import { expect } from 'e2e';
import { resultsFor } from '../support/headline.ts';
import { WRITES_ONLY_LOCALLY, runWord, writable } from '../support/target.ts';

// Journey 9 of Spec 26: a pasted note is found by search. The form's rules
// are owned by quivr-search/tests/demo.spec.ts and feed.spec.ts.
test('a pasted note is found by search', { session: 'demo', tags: ['local'] }, async ({ app, screen }) => {
  test.skip(!writable(app.baseUrl), WRITES_ONLY_LOCALLY);
  const word = runWord();
  await app.open('/');
  await screen.getByRole('button', 'Ajouter du texte').first().tap();
  const dialog = screen.getByRole('dialog', 'Ajouter du texte');
  await dialog.getByLabel('Votre texte').fill(`Note ${word}\nUne note collée par un parcours de test.`);
  await dialog.getByRole('button', 'Ajouter à la démo').tap();
  await expect(dialog.getByText('Disponible pour la recherche')).toBeVisible({ timeout: 30_000 });
  await dialog.getByRole('button', 'Fermer l’ajout de texte').tap();

  const box = screen.getByRole('searchbox', 'Rechercher dans le fil');
  await box.fill(word);
  await box.press('Enter');
  await expect(screen.getByRole('heading', resultsFor(word))).toBeVisible({ timeout: 15_000 });
});
