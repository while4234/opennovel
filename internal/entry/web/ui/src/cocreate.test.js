import { describe, expect, it } from 'vitest';
import {
  applyCoCreateSuggestion,
  appendCoCreateInput,
  coCreateStateFromError,
  coCreateStateFromEvent,
  coCreateStateFromResponse,
  createCoCreateState,
  shouldShowGlobalCoCreate,
  visibleCoCreateSuggestions
} from './cocreate.js';

describe('co-create UI state', () => {
  it('fills a clicked suggestion into editable input', () => {
    const state = {
      ...createCoCreateState(),
      suggestions: ['加强女主线', '改成双主角']
    };

    const next = applyCoCreateSuggestion(state, state.suggestions[1]);
    const edited = appendCoCreateInput(next, `${next.input}，但保留慢热节奏`);

    expect(next.input).toBe('改成双主角');
    expect(edited.input).toBe('改成双主角，但保留慢热节奏');
  });

  it('tracks whether pending input came from a suggestion', () => {
    const suggested = applyCoCreateSuggestion(createCoCreateState(), 'keep the bittersweet ending');
    const edited = appendCoCreateInput(suggested, 'keep the bittersweet ending, but soften the final scene');

    expect(suggested.input).toBe('keep the bittersweet ending');
    expect(suggested.inputSource).toBe('suggestion');
    expect(edited.inputSource).toBe('custom');
  });

  it('accepts free input without requiring a suggestion', () => {
    const state = appendCoCreateInput(createCoCreateState(), '我想要更强的宿命感');

    expect(state.input).toBe('我想要更强的宿命感');
  });

  it('preserves ready draft and locked adapt mode from backend response', () => {
    const state = coCreateStateFromResponse({
      cocreate: {
        kind: 'adapt',
        draft_prompt: '## 改编 brief',
        ready: true,
        suggestions: [],
        adapt_mode: 'arc',
        rewrite_policy: 'full_rewrite',
        mode_locked: true
      }
    });

    expect(state.status).toBe('ready');
    expect(state.draftPrompt).toBe('## 改编 brief');
    expect(state.adaptMode).toBe('arc');
    expect(state.rewritePolicy).toBe('full_rewrite');
    expect(state.modeLocked).toBe(true);
  });

  it('allows a draft prompt to be started even when the model keeps ready false', () => {
    const state = coCreateStateFromResponse({
      cocreate: {
        kind: 'adapt',
        active: true,
        draft_prompt: '## 改编 brief\n- 已经可以执行',
        ready: false,
        suggestions: []
      }
    });

    expect(state.status).toBe('ready');
    expect(state.ready).toBe(false);
    expect(state.canStart).toBe(true);
    expect(state.draftPrompt).toContain('已经可以执行');
  });

  it('keeps a backend-blocked draft visible but not startable', () => {
    const state = coCreateStateFromResponse({
      cocreate: {
        kind: 'adapt',
        active: true,
        draft_prompt: '## 改编 brief\n- 需要继续合并最新意见',
        ready: true,
        can_start: false,
        suggestions: []
      }
    });

    expect(state.status).toBe('waiting');
    expect(state.ready).toBe(true);
    expect(state.canStart).toBe(false);
    expect(state.draftPrompt).toContain('继续合并');
  });

  it('surfaces pending adaptation briefing decisions before draft generation', () => {
    const state = coCreateStateFromResponse({
      cocreate: {
        kind: 'adapt',
        active: true,
        can_start: false,
        briefing: {
          active: true,
          pending_decision_count: 1,
          total_decision_count: 1
        },
        pending_decisions: [
          {
            id: 'q1',
            question: 'How should the side romance be handled?',
            evidence: 'chapter 90 confession risk',
            impact: 'changes relationship cleanup',
            options: [{ id: 'a', label: 'Remove ambiguity' }]
          }
        ],
        suggestions: []
      }
    });

    expect(state.status).toBe('deciding');
    expect(state.canStart).toBe(false);
    expect(state.pendingDecisions).toHaveLength(1);
    expect(state.briefing.pending_decision_count).toBe(1);
  });

  it('keeps failed co-create sessions resumable', () => {
    const state = coCreateStateFromResponse({
      cocreate: {
        kind: 'adapt',
        active: true,
        failed: true,
        can_start: false,
        messages: [{ id: 'm1', role: 'user', content: 'keep the saved intent' }],
        pending_decisions: [],
        suggestions: []
      }
    });

    expect(state.status).toBe('error');
    expect(state.failed).toBe(true);
    expect(state.active).toBe(true);
    expect(state.messages).toHaveLength(1);
  });

  it('preserves editable message metadata from backend response', () => {
    const state = coCreateStateFromResponse({
      cocreate: {
        kind: 'normal',
        active: true,
        messages: [
          { id: 'm1', role: 'user', content: '写月城悬疑', editable: true, source: 'custom' },
          { id: 'm2', role: 'assistant', content: '先确认主角。' },
          { id: 'm3', role: 'user', content: '加强女主线', editable: true, source: 'suggestion' }
        ],
        ready: false,
        suggestions: []
      }
    });

    expect(state.messages[0]).toMatchObject({ id: 'm1', editable: true, source: 'custom' });
    expect(state.messages[2]).toMatchObject({ id: 'm3', editable: true, source: 'suggestion' });
  });

  it('shows parsed suggestions while waiting for user input', () => {
    const state = coCreateStateFromResponse({
      cocreate: {
        kind: 'adapt',
        active: true,
        messages: [
          { id: 'm1', role: 'system', content: 'adapt co-create started' },
          { id: 'm2', role: 'assistant', content: '请选择方向。' }
        ],
        ready: false,
        suggestions: [
          '保持黑暗虐心但稍微调整女主心理线',
          '改成双向救赎结局，让女主活下来',
          '削弱性虐尺度，加强情感拉扯'
        ]
      }
    });

    expect(state.status).toBe('waiting');
    expect(state.suggestions).toEqual([
      '保持黑暗虐心但稍微调整女主心理线',
      '改成双向救赎结局，让女主活下来',
      '削弱性虐尺度，加强情感拉扯'
    ]);
  });

  it('does not invent suggestions from assistant prose', () => {
    const suggestions = visibleCoCreateSuggestions({
      messages: [
        {
          role: 'assistant',
          content: [
            '好的，我来规划整体篇章结构。',
            '整体节奏为黑暗揭露（前两卷）-> 真相挣扎（中卷）-> 治愈蜕变（后卷）-> 悬念收尾（终章）。',
            '看看这个规划是否符合你的预期，或者需要调整卷与卷之间的篇幅。'
          ].join('\n')
        }
      ],
      suggestions: null
    });

    expect(suggestions).toEqual([]);
  });

  it('merges stream progress without duplicating assistant messages or clearing errors', () => {
    const previous = {
      ...createCoCreateState(),
      active: true,
      error: 'previous error',
      messages: [{ role: 'user', content: '写一个月城悬疑' }]
    };

    const state = coCreateStateFromEvent({
      type: 'cocreate_state',
      cocreate: {
        kind: 'normal',
        active: true,
        messages: previous.messages,
        stream_thinking: 'checking premise',
        stream_reply: '先确认主角目标',
        ready: false,
        suggestions: []
      }
    }, previous);

    expect(state.status).toBe('running');
    expect(state.error).toBe('previous error');
    expect(state.messages).toEqual(previous.messages);
    expect(state.streamThinking).toBe('checking premise');
    expect(state.streamReply).toBe('先确认主角目标');
  });

  it('keeps backend co-create state on begin or send errors', () => {
    const previous = {
      ...createCoCreateState(),
      input: 'retry text'
    };
    const error = new Error('stream failed');
    error.data = {
      cocreate: {
        kind: 'stage',
        active: true,
        messages: [{ role: 'system', content: 'stage paused' }],
        draft_prompt: '',
        ready: false,
        suggestions: []
      }
    };

    const state = coCreateStateFromError(error, previous);

    expect(state.status).toBe('error');
    expect(state.error).toBe('stream failed');
    expect(state.active).toBe(true);
    expect(state.kind).toBe('stage');
    expect(state.input).toBe('retry text');
  });

  it('shows global co-create only for first normal/adaptation creation or unfinished recovery', () => {
    expect(shouldShowGlobalCoCreate({ snapshot: null, coCreate: null, planningReview: null })).toBe(true);
    expect(shouldShowGlobalCoCreate({ snapshot: { phase: 'init' }, coCreate: createCoCreateState() })).toBe(true);
    expect(shouldShowGlobalCoCreate({ snapshot: { phase: 'writing' }, coCreate: { ...createCoCreateState(), status: 'started' } })).toBe(false);
    expect(shouldShowGlobalCoCreate({ snapshot: { phase: 'writing' }, coCreate: { ...createCoCreateState(), kind: 'adapt', active: true } })).toBe(true);
    expect(shouldShowGlobalCoCreate({ snapshot: { phase: 'writing' }, coCreate: { ...createCoCreateState(), kind: 'normal', failed: true, status: 'error' } })).toBe(true);
    expect(shouldShowGlobalCoCreate({ snapshot: { phase: 'outline' }, coCreate: { ...createCoCreateState(), kind: 'continuation', active: true } })).toBe(false);
    expect(shouldShowGlobalCoCreate({ snapshot: { phase: 'writing' }, coCreate: createCoCreateState(), planningReview: { active: true } })).toBe(true);
  });
});
