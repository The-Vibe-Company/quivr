import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5182,
    strictPort: true,
    proxy: { "/v0": "http://127.0.0.1:5183", "/demo": "http://127.0.0.1:5183" },
  },
});
