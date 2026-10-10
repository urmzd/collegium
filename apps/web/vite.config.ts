import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

// The server from `make dev`. The console never talks to it cross-origin: the
// dev server proxies, so the session cookie and the cross-origin check behave
// exactly as they do when the server serves the built console itself.
const api = process.env.OAP_API ?? "http://127.0.0.1:8080";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    // agent-ui is linked from a sibling checkout with its own node_modules.
    // Without this, it would load a second copy of React.
    dedupe: ["react", "react-dom"],
  },
  server: {
    host: "127.0.0.1",
    port: 5173,
    fs: { allow: ["../..", "../../../agent-ui"] },
    proxy: {
      "/api": { target: api, changeOrigin: false },
      "/auth": { target: api, changeOrigin: false },
    },
  },
});
