/// <reference types="vitest/config" />
import { writeFileSync } from "node:fs";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig, type Plugin } from "vite";

// emptyOutDir wipes dist/.gitkeep on every build, but that file has to stay
// tracked or `go build ./...` fails on a clean checkout (//go:embed all:dist
// needs at least one file). Put it back once the bundle is written.
function keepDistGitkeep(): Plugin {
  return {
    name: "keep-dist-gitkeep",
    closeBundle() {
      writeFileSync(new URL("./dist/.gitkeep", import.meta.url), "");
    },
  };
}

export default defineConfig({
  plugins: [react(), tailwindcss(), keepDistGitkeep()],
  resolve: { alias: { "@": new URL("./src", import.meta.url).pathname } },
  build: { outDir: "dist", emptyOutDir: true, chunkSizeWarningLimit: 700 },
  server: {
    port: 5173,
    proxy: {
      "/api": "http://localhost:8097",
      "/healthz": "http://localhost:8097",
    },
  },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./src/test/setup.ts"],
  },
});
