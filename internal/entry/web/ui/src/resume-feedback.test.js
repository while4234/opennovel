import { describe, expect, it } from 'vitest';
import { resumeNoOpMessage } from './App.jsx';

describe('resume action feedback', () => {
  it('explains why a successful request did not start the project', () => {
    expect(resumeNoOpMessage({
      running: false,
      label: '共创建议或决策等待用户'
    })).toBe('恢复未启动：共创建议或决策等待用户');
  });

  it('stays silent when resume started the project', () => {
    expect(resumeNoOpMessage({ running: true, label: '已恢复' })).toBe('');
  });

  it('provides a fallback when the backend omits a label', () => {
    expect(resumeNoOpMessage({ running: false })).toBe('恢复未启动，请检查当前阶段后重试。');
  });
});
