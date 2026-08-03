import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

const appSource = readFileSync(new URL('./App.jsx', import.meta.url), 'utf8');

describe('workflow action placement', () => {
  it('keeps confirmation in the side panel and recovery in the workspace toolbar', () => {
    expect(appSource).not.toMatch(/<WorkflowProgressPanel[\s\S]*?onNextAction=/);
    expect(appSource).not.toContain('runWorkflowNextAction');
    expect(appSource).toMatch(/<CoCreatePanel[\s\S]*?onCommit=\{commitCoCreateFlow\}/);
    expect(appSource).toMatch(/continuationCanResume\(continuationSnapshot\)[\s\S]*?runAction\(resumeProject/);
    expect(appSource).not.toContain('恢复共创');
    expect(appSource).not.toContain('恢复当前规划任务');
  });
});
