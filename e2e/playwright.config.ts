import { defineConfig } from '@playwright/test'

const appPort = process.env.APP_PORT ?? '3000'
const proxyPort = process.env.SCD_MOCK_PROXY_PORT ?? '80'

// Runs against `docker compose up`. The browser sends every request
// for a customer domain (*.test) to the mock proxy, the way DNS sends
// a real customer domain to SaaS Custom Domains. Host and Origin stay
// "go.acme.test" whatever port the mock listens on, so several stacks
// can run side by side.
export default defineConfig({
  testDir: 'tests',
  timeout: 30_000,
  use: {
    baseURL: `http://localhost:${appPort}`,
    proxy: { server: `http://127.0.0.1:${proxyPort}`, bypass: 'localhost' },
    trace: 'retain-on-failure',
  },
})
