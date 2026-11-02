import { test } from '@e2e-dev/web';
import { expect } from 'e2e';

// Journey 2 of Spec 26: the newest article opens in the reader, named after
// its headline, and Escape closes it. A text added by hand has no original to
// link to, so the link to the original is checked only on an article from a
// source.
test('an article opens in the reader, and Escape closes it', { session: 'demo', tags: ['live'] }, async ({ app, screen }) => {
  await app.open('/');
  const first = screen.getByRole('list', 'Derniers éléments').getByRole('listitem').first();
  await expect(first).toBeVisible({ timeout: 15_000 });
  const headline = first.getByRole('heading').getByRole('link');
  const title = (await headline.textContent()) ?? '';
  await headline.tap();
  const reader = screen.getByRole('complementary', title);
  await expect(reader).toBeVisible();
  if (!(await first.getByText('Ajouté à la main').isVisible()))
    await expect(reader.getByRole('link', { name: /^Ouvrir l’original/ })).toBeVisible();
  await app.screenshot('reader');
  await reader.getByRole('heading').first().press('Escape');
  await expect(reader).toBeHidden();
});
