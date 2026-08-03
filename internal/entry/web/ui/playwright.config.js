import { defineConfig, devices } from '@playwright/test';

export default defineConfig({
  testDir: './tests/browser',
  fullyParallel: false,
  workers: 1,
  reporter: 'line',
  use: { baseURL: 'http://127.0.0.1:4179', trace: 'retain-on-failure' },
  projects: [
    { name: 'desktop', use: { ...devices['Desktop Chrome'] } },
    { name: 'mobile', use: { ...devices['Pixel 7'] } }
  ],
  webServer: [
	{ command: 'powershell -NoProfile -ExecutionPolicy Bypass -File tests/browser/start-go-expansion-server.ps1', url: 'http://127.0.0.1:4182/health', reuseExistingServer: false, timeout: 120000 },
    { command: 'node tests/browser/api-server.mjs', url: 'http://127.0.0.1:4180/health', reuseExistingServer: false, timeout: 30000 },
    { command: 'npm run dev -- --host 127.0.0.1 --port 4179', url: 'http://127.0.0.1:4179/browser-fixture.html', reuseExistingServer: false, timeout: 30000 }
  ]
});
