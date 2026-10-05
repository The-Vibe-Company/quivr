import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  build: {
    // Browsers of the last few years: regular expressions with \p{…} stay
    // literals, compiled once, instead of a new RegExp on every call.
    target: "es2022",
    rollupOptions: {
      output: {
        // React changes far less often than the app: its own chunk stays cached across deploys.
        manualChunks: (id) =>
          /node_modules\/(react|react-dom|scheduler)\//.test(id) ? "react" : undefined,
      },
    },
  },
  server: {
    port: 5182,
    strictPort: true,
    proxy: { "/v0": "http://127.0.0.1:5183", "/demo": "http://127.0.0.1:5183" },
  },
});
