import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

const app = readFileSync(new URL('./App.jsx', import.meta.url), 'utf8');

describe('adaptation audit web workflow', () => {
  it('exposes a dedicated audit tool page rather than hiding it behind restore', () => {
    expect(app).toContain("sideView === 'audit'");
    expect(app).toContain('改编质量审计');
    expect(app).toContain('生成只读审计报告');
    expect(app).toContain('确认应用修复计划');
  });

  it('keeps applying a repair separate from project resume', () => {
    const start = app.indexOf('const applyAdaptationAuditRepair = async () =>');
    const end = app.indexOf('useEffect(() => {', start);
    expect(start).toBeGreaterThanOrEqual(0);
    expect(app.slice(start, end)).not.toContain('resumeProject(');
    expect(app).toContain('不会立即改写正文，也不会自动恢复项目');
  });

	it('uses one source endpoint picker and derives every other boundary', () => {
		expect(app).toContain('原著结束章');
		expect(app).toContain('adaptation-audit-source-chapters');
		expect(app).toContain('chapter.title');
		expect(app).not.toContain('原著起始章');
		expect(app).not.toContain('改编起始章');
		expect(app).not.toContain('改编结束章');
	});
});
