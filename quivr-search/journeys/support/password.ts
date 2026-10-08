import { execFileSync } from 'node:child_process';

/**
 * The demo password for `origin`: QUIVR_DEMO_PASSWORD, else the macOS keychain
 * item of service `quivr-demo` and account `origin`, else empty (a demo
 * without a password). `security` prints a password with non-ASCII characters
 * as hex, so keep a keychain password ASCII. Read only by the sign-in setup,
 * never by the model.
 */
export function demoPassword(origin: string): string {
  if (process.env.QUIVR_DEMO_PASSWORD) return process.env.QUIVR_DEMO_PASSWORD;
  if (process.platform !== 'darwin') return '';
  try {
    return execFileSync('security', ['find-generic-password', '-s', 'quivr-demo', '-a', origin, '-w'], {
      encoding: 'utf8',
      stdio: ['ignore', 'pipe', 'ignore'],
    }).replace(/\n$/, '');
  } catch {
    return '';
  }
}
