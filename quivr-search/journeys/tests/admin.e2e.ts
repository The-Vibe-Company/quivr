import { test } from '@e2e-dev/web';
import { expect } from 'e2e';

// Journey 10 of Spec 26: the Admin tab shows the pipeline's steps. Without
// observability:read on the deployment's key it fails: a deployment finding,
// not a test to relax. Health rules are owned by quivr-search/tests/admin.spec.ts.
test('the Admin tab shows each step of the pipeline', { session: 'demo', tags: ['live'] }, async ({ app, screen }) => {
  await app.open('/?view=admin');
  await expect(screen.getByRole('heading', { name: 'Admin', level: 1 })).toBeVisible({ timeout: 15_000 });
  await expect(screen.getByRole('list', 'Étapes, dans l’ordre').getByRole('listitem').first()).toBeVisible({ timeout: 15_000 });
  await app.screenshot('admin');
});
