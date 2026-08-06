import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

const appSource = readFileSync(new URL('../App.jsx', import.meta.url), 'utf8');

describe('settings action wiring', () => {
  it('keeps provider and model pages on the existing stateful action handlers', () => {
    expect(appSource).toContain("modelSettingsPanel('providers')");
    expect(appSource).toContain("modelSettingsPanel('models')");
    for (const handler of ['switchDefaultModel', 'switchModelRoute', 'inheritModelRoute', 'changeThinking', 'changeRetrySettings', 'submitCustomModel', 'discoverCustomModelModels', 'testCustomModelConnection', 'startGrokOAuthLogin', 'uploadCodexCredential']) {
      expect(appSource).toContain(`={${handler}}`);
    }
  });

  it('reuses the global schedule and project backend commands', () => {
    expect(appSource).toContain('onRefresh={loadResumeSchedule}');
    expect(appSource).toContain('onSave={saveResumeSchedule}');
    expect(appSource).toContain('onRefresh={refreshBackendStatus}');
    expect(appSource).toContain('onTest={runBackendTest}');
  });
});
