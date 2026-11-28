import fs from "node:fs";
import path from "node:path";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

function preserveGitkeepPlugin() {
  return {
    name: "preserve-dist-gitkeep",
    closeBundle() {
      const distDir = path.resolve(__dirname, "dist");
      fs.mkdirSync(distDir, { recursive: true });
      fs.writeFileSync(path.join(distDir, ".gitkeep"), "");
    },
  };
}

export default defineConfig({
  plugins: [react(), tailwindcss(), preserveGitkeepPlugin()],
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./src"),
    },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
  server: {
    port: 5173,
    proxy: {
      "/api": {
        target: "http://127.0.0.1:17070",
        changeOrigin: true,
      },
    },
  },
});
