import { test } from '@e2e-dev/web';
import { expect } from 'e2e';

// Journey 4 of Spec 26: the Explorer counts documents and opens one with its
// versions. Facets, keyboard and address state are owned by
// quivr-search/tests/explorer.spec.ts.
test('the Explorer counts documents and opens one with its versions', { session: 'demo', tags: ['live'] }, async ({ app, screen }) => {
  await app.open('/?view=explorer');
  const count = screen.getByRole('status').filter({ hasText: 'document' });
  await expect(count).toHaveText(/^[1-9][\d\s\u00a0\u202f]* documents?$/, { timeout: 15_000 });
  await screen.getByRole('listbox', 'Documents').getByRole('option').first().tap();
  await expect(screen.getByRole('complementary', 'Aperçu').getByRole('region', { name: /^Versions/ })).toBeVisible({ timeout: 15_000 });
  await app.screenshot('explorer');
});
