import { describe, expect, it } from 'vitest';
import {
  adaptationAuditApplicationText,
  adaptationAuditScopeText,
  buildAdaptationAuditApplyRequest,
  buildAdaptationAuditOptions,
  defaultAdaptationAuditScope,
	normalizedAuditSourceChapters
} from './adaptation-audit.js';

describe('adaptation audit UI contracts', () => {
  it('sends only the optional source endpoint and lets the backend derive target bounds', () => {
    expect(buildAdaptationAuditOptions(defaultAdaptationAuditScope)).toEqual({
		ok: true,
		options: {}
    });
	expect(buildAdaptationAuditOptions({ sourceTo: '284' })).toEqual({ ok: true, options: { source_to: 284 } });
	expect(buildAdaptationAuditOptions({ sourceTo: '1.5' })).toMatchObject({ ok: false });
  });

	it('normalizes source chapter choices without exposing manifest metadata', () => {
		expect(normalizedAuditSourceChapters([
		  { chapter: 2, title: '第二章', path: 'private' },
		  { chapter: 1, title: ' 第一章 ' },
		  { chapter: 2, title: '重复' },
		  { chapter: 0, title: '无效' }
		])).toEqual([
		  { chapter: 1, title: '第一章' },
		  { chapter: 2, title: '第二章' }
		]);
	});

  it('requires an acknowledgement before submitting every blocking finding id', () => {
    const report = {
      digest: 'report-1',
      confirmation: {
        required: true,
        blocking_finding_ids: ['missing-meet', 'missing-kidnap']
      }
    };
    expect(buildAdaptationAuditApplyRequest(report, false)).toMatchObject({ ok: false });
    expect(buildAdaptationAuditApplyRequest(report, true)).toEqual({
      ok: true,
      confirmation: {
        report_digest: 'report-1',
        decision: 'apply',
        acknowledged_finding_ids: ['missing-meet', 'missing-kidnap']
      }
    });
  });

  it('describes scope and queued repair without claiming that prose is already rewritten', () => {
	expect(adaptationAuditScopeText({ source_from: 1, source_to: 284, target_from: 1, target_to: 315 })).toBe('原著 1–284 / 改编 1–315');
    expect(adaptationAuditApplicationText({ queued_chapters: [1, 2] })).toContain('点击顶部“恢复”执行');
    expect(adaptationAuditApplicationText({ queued_chapters: [1, 2] })).not.toContain('已改写正文');
  });
});
