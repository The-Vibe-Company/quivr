import type { AgentOptions } from 'e2e';
import { gateway } from 'ai';
import { openai } from '@ai-sdk/openai';
import { chatgpt } from 'e2e/oauth/chatgpt';

const PROVIDERS = {
  // A ChatGPT subscription, after `npx e2e login openai`.
  chatgpt,
  // The Vercel AI Gateway: AI_GATEWAY_API_KEY or a linked Vercel project.
  gateway,
  // The OpenAI API: OPENAI_API_KEY.
  openai,
};

/**
 * The agent model, chosen by QUIVR_JOURNEYS_MODEL as `<provider>:<model id>`,
 * for example `chatgpt:gpt-6-luna`. Unset, the agent has no model: steps
 * without the agent still run, and an agent step fails with MODEL_UNAVAILABLE.
 */
export function agentModel(): Pick<AgentOptions, 'model'> {
  const choice = process.env.QUIVR_JOURNEYS_MODEL;
  if (!choice) return {};
  const [provider, ...rest] = choice.split(':');
  const id = rest.join(':');
  if (!(provider in PROVIDERS) || !id)
    throw new Error(
      `QUIVR_JOURNEYS_MODEL=${choice}: write <provider>:<model id> with a provider among ${Object.keys(PROVIDERS).join(', ')}`,
    );
  return { model: PROVIDERS[provider as keyof typeof PROVIDERS](id) };
}
