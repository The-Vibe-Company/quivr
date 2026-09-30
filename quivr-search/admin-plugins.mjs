// The Admin tab's plugin list (THE-797): the plugin versions the engine's
// active plan runs and the roles each serves (GET /v0/admin/active-plugins,
// observability:read), relayed read-only and cached a few seconds. The
// engine names no address or configuration there, and neither does this.

const CACHE_MS = 10000;

export function activePlugins({ upstream, clock = Date.now }) {
  let cached;
  return async function read() {
    if (cached && clock() - cached.at < CACHE_MS) return cached.response;
    const pending = upstream("/v0/admin/active-plugins").then((response) => {
      if (response.status === 403)
        return {
          status: 403,
          data: {
            code: "demo_request_failed",
            message:
              "Le suivi n’est pas activé sur ce déploiement : la clé du moteur n’a pas le droit observability:read (QUIVR_DEMO_ADMIN).",
            retryable: false,
          },
        };
      // A core without a plugin registry runs no plugin.
      if (response.status === 404) return { status: 200, data: { items: [] } };
      if (response.status !== 200) return response;
      return {
        status: 200,
        data: {
          plan_activated_at: response.data.plan_activated_at,
          items: (response.data.items || []).map((p) => ({
            plugin_id: p.plugin_id,
            version: p.version,
            roles: p.roles,
          })),
        },
      };
    });
    cached = { at: clock(), response: pending };
    try {
      return await pending;
    } catch (error) {
      cached = undefined;
      throw error;
    }
  };
}
