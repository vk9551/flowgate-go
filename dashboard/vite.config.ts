import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  base: '/dashboard/',
  build: {
    outDir: '../internal/dashboard/dist',
    emptyOutDir: true,
  },
  server: {
    port: 5173,
    proxy: {
      '/v1': {
        target: 'http://localhost:7700',
        changeOrigin: true,
      },
    },
  },
})
