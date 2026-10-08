import { test } from '@e2e-dev/web';
import { expect } from 'e2e';

// Journey 1 of Spec 26: a signed-in visitor sees the Fil. Live data moves, so
// it checks presence only: an open moment heading and an article under it.
test('the Fil shows articles under a moment heading', { session: 'demo', tags: ['live'] }, async ({ app, screen }) => {
  await app.open('/');
  const timeline = screen.getByRole('list', 'Derniers éléments');
  await expect(timeline.getByRole('button', { expanded: true }).first()).toBeVisible({ timeout: 15_000 });
  await expect(timeline.getByRole('listitem').getByRole('heading', { level: 3 }).first()).toBeVisible();
  await app.screenshot('feed');
});
