import { svelte } from '@sveltejs/vite-plugin-svelte';
import { defineConfig, loadEnv } from 'vite';

export default defineConfig(({ mode }) => {
  const environment = loadEnv(mode, '.', 'Q_STUDIO_');
  return {
    plugins: [svelte()],
    build: {
      outDir: 'dist',
      emptyOutDir: true,
      sourcemap: false
    },
    server: {
      proxy: {
        '/api': environment.Q_STUDIO_API ?? 'http://127.0.0.1:7070'
      }
    }
  };
});
