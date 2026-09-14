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
      // A project page opened as http://localhost:5173/p/<id>/ carries the
      // project's API base in its path; those calls go to the control plane,
      // which forwards them to the project. Only the API is proxied — the page
      // and its /src/* modules stay with Vite so HMR keeps working (a key
      // starting with ^ is matched as a regex).
      '^/p/[^/]+/api': {
        target: 'http://localhost:9527',
        changeOrigin: true,
      },
    },
  },
})
