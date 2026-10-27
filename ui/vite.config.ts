import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'

// The dev server proxies the API to a local awxnext (AWXNEXT_URL, default :8052).
const target = process.env.AWXNEXT_URL ?? 'http://localhost:8052'

export default defineConfig({
  plugins: [react()],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    sourcemap: false,
    chunkSizeWarningLimit: 900,
    // Fonts stay files: the server's CSP does not allow data: fonts.
    assetsInlineLimit: (file) => (/\.(woff2?|ttf)$/.test(file) ? false : undefined),
  },
  server: {
    // Let OPTIONS requests reach the API (Vite answers them itself when CORS is on).
    cors: false,
    proxy: {
      '/api': target,
      '/sso': target,
      '/websocket': { target, ws: true },
    },
  },
  test: { environment: 'node' },
})
