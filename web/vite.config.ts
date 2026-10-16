import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// DEV MODE: the Go server runs with -dev-user alice (or whoever), and the
// proxy injects X-Remote-User so auth is simulated without OpenCloud.
export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    port: 5173,
    proxy: {
      "/caldav": {
        target: "http://127.0.0.1:5232",
        changeOrigin: true,
        headers: { "x-remote-user": process.env.DAVCLOUD_DEV_USER ?? "alice" },
      },
      "/carddav": {
        target: "http://127.0.0.1:5232",
        changeOrigin: true,
        headers: { "x-remote-user": process.env.DAVCLOUD_DEV_USER ?? "alice" },
      },
    },
  },
  build: { outDir: "dist" },
});
