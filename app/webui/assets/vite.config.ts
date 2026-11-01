import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  // Relative asset URLs. The same index.html is served from two places — the
  // control plane at "/" and each project's WebUI at "/p/<id>/" — so a
  // hard-coded /assets/… resolves against the control origin even on a project
  // page. That ties every running project's HTML to the control binary's
  // current bundle name: upgrade control (or a project) and any older page
  // requests a hashed file nobody serves any more, which the browser refuses
  // as a text/plain module. "./assets/…" keeps each page loading the bundle
  // shipped by the process that served it.
  base: './',
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
