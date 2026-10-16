import { createServer } from "vite";

// The runner is plain node, which reads .ts but not JSX. Vite is already here
// and already knows how to load this app, linked vultus packages included,
// so it loads the .test.tsx files. Their tests register with this file's run.
const files = ["/src/ui.test.tsx", "/src/home.test.tsx", "/src/usage.test.tsx"];

const server = await createServer({
  configFile: false,
  root: new URL("..", import.meta.url).pathname,
  logLevel: "error",
  appType: "custom",
  resolve: { dedupe: ["react", "react-dom"] },
  server: { middlewareMode: true, hmr: false, ws: false, watch: null },
  optimizeDeps: { noDiscovery: true, include: [] },
});
try {
  for (const file of files) await server.ssrLoadModule(file);
} finally {
  await server.close();
}
