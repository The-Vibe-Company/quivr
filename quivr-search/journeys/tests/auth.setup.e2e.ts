import { test } from '@e2e-dev/web';
import { expect } from 'e2e';
import { demoPassword } from '../support/password.ts';

// Signs in once for every journey. The runner posts the password itself and
// hands the browser the session cookie: the model and the page never see the
// password, and screenshots stay available, which a password typed into the
// page would withhold for the rest of the run. The password form is owned by
// quivr-search/tests/demo.spec.ts, wrong passwords by tests/access.spec.ts.
test.setup('sign in to the demo', { sessions: ['demo'] }, async ({ app, browser, screen, session }) => {
  const base = app.baseUrl!;
  // Opened first so that a demo that is not running fails here, naming its address.
  await app.open('/');
  const password = demoPassword(new URL(base).origin);
  if (password) {
    const login = new URL('/demo/login', base);
    const response = await fetch(login, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ password }),
    });
    if (!response.ok) throw new Error(`POST ${login} answered ${response.status}: check the demo password`);
    const value = response.headers
      .getSetCookie()
      .map((cookie) => cookie.split(';')[0])
      .find((cookie) => cookie.startsWith('quivr_demo='))
      ?.slice('quivr_demo='.length);
    if (!value) throw new Error(`POST ${login} set no quivr_demo cookie`);
    await browser.setCookies([{ url: base, name: 'quivr_demo', value, httpOnly: true, sameSite: 'Strict' }]);
    await app.open('/');
  }
  const fil = screen.getByRole('navigation', 'Sections').getByRole('link', 'Fil');
  try {
    await expect(fil).toHaveAttribute('aria-current', 'page', { timeout: 15_000 });
  } catch (error) {
    if (await screen.getByLabel('Mot de passe').isVisible())
      throw new Error(
        `${base} asks for a password: set QUIVR_DEMO_PASSWORD, or store it in the keychain (service quivr-demo, account ${new URL(base).origin})`,
      );
    throw error;
  }
  await session.save('demo');
});
