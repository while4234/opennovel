import { fileURLToPath, URL } from 'node:url';

import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

const uiRoot = fileURLToPath(new URL('../internal/entry/web/ui', import.meta.url));

export default defineConfig({
  root: uiRoot,
  plugins: [react()],
  build: {
    outDir: '../static',
    emptyOutDir: true,
    sourcemap: false
  },
  test: {
    environment: 'node'
  }
});
