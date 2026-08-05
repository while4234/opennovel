import { defineConfig, devices } from '@playwright/test';

const { defaultBrowserType: _defaultBrowserType, ...iphone15ProMax } = devices['iPhone 15 Pro Max'];
const mobileBrowser = process.env.PLAYWRIGHT_MOBILE_CHANNEL
  ? { browserName: 'chromium', channel: process.env.PLAYWRIGHT_MOBILE_CHANNEL }
  : { browserName: 'webkit' };

export default defineConfig({
  testDir: './tests/browser',
  fullyParallel: false,
  workers: 1,
  reporter: 'line',
  use: { baseURL: 'http://127.0.0.1:4179', trace: 'retain-on-failure' },
  projects: [
    { name: 'desktop', use: { ...devices['Desktop Chrome'] } },
    {
      name: 'mobile',
      use: {
        ...iphone15ProMax,
        ...mobileBrowser,
        viewport: { width: 430, height: 932 },
        screen: { width: 430, height: 932 }
      }
    },
    {
      name: 'mobile-landscape',
      use: {
        ...iphone15ProMax,
        ...mobileBrowser,
        viewport: { width: 932, height: 430 },
        screen: { width: 932, height: 430 }
      }
    }
  ],
  webServer: [
	{ command: 'powershell -NoProfile -ExecutionPolicy Bypass -File tests/browser/start-go-expansion-server.ps1', url: 'http://127.0.0.1:4182/health', reuseExistingServer: false, timeout: 120000 },
    { command: 'node tests/browser/api-server.mjs', url: 'http://127.0.0.1:4180/health', reuseExistingServer: false, timeout: 30000 },
    { command: 'npm run dev -- --host 127.0.0.1 --port 4179', url: 'http://127.0.0.1:4179/browser-fixture.html', reuseExistingServer: false, timeout: 30000 }
  ]
});
