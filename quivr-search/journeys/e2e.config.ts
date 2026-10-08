import type { E2EConfig } from 'e2e';
import { web } from '@e2e-dev/web';
import { agentModel } from './support/model.ts';

// The demo is started outside the run: `make demo` locally, or the deployed demo.
export default {
  targets: [
    {
      name: 'demo',
      engine: web({ browser: 'chromium' }),
      app: { url: process.env.QUIVR_DEMO_URL || 'http://127.0.0.1:5183' },
    },
  ],
  // A journey that fails without a code change gets a ticket, never a retry (docs/agents/testing.md).
  retries: 0,
  agents: { default: agentModel() },
} satisfies E2EConfig;
