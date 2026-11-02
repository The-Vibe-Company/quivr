import { test } from '@e2e-dev/web';
import { expect } from 'e2e';
import { headlineWord, resultsFor } from '../support/headline.ts';

// Journey 3 of Spec 26: a word from a visible headline finds articles, with
// "Idées proches" on and off. Search modes themselves are owned by
// quivr-search/tests/dashboard.spec.ts.
test('a word from a headline finds articles, with and without close ideas', { session: 'demo', tags: ['live'] }, async ({ app, screen }) => {
  await app.open('/');
  const word = await headlineWord(screen);
  const box = screen.getByRole('searchbox', 'Rechercher dans le fil');
  await box.fill(word);
  await box.press('Enter');
  const results = screen.getByRole('heading', resultsFor(word));
  await expect(results).toBeVisible({ timeout: 15_000 });

  const close = screen.getByRole('switch', 'Idées proches');
  const before = await close.isChecked();
  await close.tap();
  await expect(close).toHaveAttribute('aria-checked', String(!before));
  await expect(results).toBeVisible({ timeout: 15_000 });
  await app.screenshot('search');
});
