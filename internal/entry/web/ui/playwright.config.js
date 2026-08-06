import { defineConfig, devices } from '@playwright/test';

const { defaultBrowserType: _defaultBrowserType, ...iphone15ProMax } = devices['iPhone 15 Pro Max'];
const requestedChannel = process.env.PLAYWRIGHT_CHANNEL || '';
const mobileChannel = process.env.PLAYWRIGHT_MOBILE_CHANNEL || requestedChannel;
const desktopBrowser = requestedChannel
  ? { browserName: 'chromium', channel: requestedChannel }
  : devices['Desktop Chrome'];
const mobileBrowser = mobileChannel
  ? { browserName: 'chromium', channel: mobileChannel }
  : { browserName: 'webkit' };
const uiOnly = process.env.PLAYWRIGHT_UI_ONLY === '1';
const externalBaseURL = process.env.PLAYWRIGHT_BASE_URL || '';
const browserFixtureServer = {
  command: 'npm run dev -- --host 127.0.0.1 --port 4179',
  url: 'http://127.0.0.1:4179/browser-fixture.html',
  reuseExistingServer: false,
  timeout: 180000
};

export default defineConfig({
  testDir: './tests/browser',
  fullyParallel: false,
  workers: 1,
  reporter: 'line',
  use: { baseURL: externalBaseURL || 'http://127.0.0.1:4179', trace: 'retain-on-failure' },
  projects: [
    { name: 'desktop', use: { ...desktopBrowser } },
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
  webServer: externalBaseURL ? undefined : uiOnly ? [browserFixtureServer] : [
	{ command: 'powershell -NoProfile -ExecutionPolicy Bypass -File tests/browser/start-go-expansion-server.ps1', url: 'http://127.0.0.1:4182/health', reuseExistingServer: false, timeout: 120000 },
    { command: 'node tests/browser/api-server.mjs', url: 'http://127.0.0.1:4180/health', reuseExistingServer: false, timeout: 30000 },
    browserFixtureServer
  ]
});
