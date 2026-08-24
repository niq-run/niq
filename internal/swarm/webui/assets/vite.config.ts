import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      // Dev server (on :5173) always talks to the control plane on :9527,
      // regardless of its own port.
      '/api': {
        target: 'http://localhost:9527',
        changeOrigin: true,
      },
    },
  },
})
