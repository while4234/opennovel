import { describe, expect, it } from 'vitest';
import {
  appendStreamDelta,
  compactStreamRounds,
  createWorkbenchState,
  mergeSnapshotUpdate,
  mergeWorkflowProgress,
  mergeEventRows,
  preserveOutlineDetails,
  preservePlanningReviewDetails,
  reduceWebEvent,
  reduceWebEvents,
  startStreamRound
} from './events.js';

describe('web event reducer', () => {
  it('updates running host events with the same id in place', () => {
    const started = {
      seq: 1,
      type: 'host_event',
      host_event_id: 'tool-1',
      event: { id: 'tool-1', summary: 'drafting', running: true }
    };
    const finished = {
      seq: 2,
      type: 'host_event',
      host_event_id: 'tool-1',
      event: { id: 'tool-1', summary: 'drafted', running: false }
    };

    const rows = mergeEventRows(mergeEventRows([], started), finished);

    expect(rows).toHaveLength(1);
    expect(rows[0].event.summary).toBe('drafted');
    expect(rows[0].event.running).toBe(false);
  });

  it('ignores duplicate seq events after reconnect', () => {
    const initial = createWorkbenchState();
    const first = reduceWebEvent(initial, {
      seq: 4,
      type: 'stream_delta',
      stream: { text: 'alpha' }
    });
    const duplicate = reduceWebEvent(first, {
      seq: 4,
      type: 'stream_delta',
      stream: { text: 'alpha' }
    });

    expect(duplicate.streamRounds[0].text).toBe('alpha');
  });

  it('accepts a newer sparse sequence after replay compaction', () => {
    const initial = reduceWebEvent(createWorkbenchState(), {
      seq: 4,
      type: 'host_event',
      host_event_id: 'tool-1',
      event: { id: 'tool-1', summary: 'drafting', running: true }
    });
    const compactedReplay = reduceWebEvent(initial, {
      seq: 9,
      type: 'snapshot',
      snapshot: { runtime_state: 'running', completed_count: 5 }
    });

    expect(compactedReplay.lastSeq).toBe(9);
    expect(compactedReplay.snapshot.completed_count).toBe(5);
  });

  it('keeps top-level SSE workflow progress inside the current snapshot', () => {
    const workflowProgress = {
      workflow: 'continuation',
      status: 'running',
      steps: [{ id: 'writing', label: '续写正文', status: 'running' }]
    };
    const next = reduceWebEvent(createWorkbenchState(), {
      seq: 1,
      type: 'snapshot',
      snapshot: { runtime_state: 'running' },
      workflow_progress: workflowProgress
    });

    expect(next.snapshot.runtime_state).toBe('running');
    expect(next.snapshot.workflow_progress).toBe(workflowProgress);
    expect(mergeWorkflowProgress(null, workflowProgress)).toEqual({ workflow_progress: workflowProgress });
  });

  it('keeps the latest workflow progress when a legacy snapshot update omits it', () => {
    const workflowProgress = {
      workflow: 'normal',
      status: 'running',
      steps: [{ id: 'writing', label: '正文创作', status: 'running' }]
    };
    const current = {
      runtime_state: 'running',
      completed_count: 3,
      workflow_progress: workflowProgress
    };

    const next = mergeSnapshotUpdate(current, {
      runtime_state: 'running',
      completed_count: 4
    });

    expect(next.completed_count).toBe(4);
    expect(next.workflow_progress).toBe(workflowProgress);
  });

  it('preserves full chapter detail when a compact SSE snapshot updates counters', () => {
    const previous = {
      Outline: [{
        Chapter: 23,
        Title: '预审名单上的空位',
        CoreEvent: '完整核心事件',
        Hook: '完整钩子',
        Scenes: ['场景一', '场景二'],
        WordBudget: { TargetWords: 3000 }
      }]
    };
    const incoming = {
      RuntimeState: 'idle',
      Outline: [{
        Chapter: 23,
        Title: '预审名单上的空位',
        CoreEvent: '',
        Hook: '',
        Scenes: null,
        WordBudget: { TargetWords: 3030 }
      }]
    };

    const next = preserveOutlineDetails(previous, incoming);

    expect(next.RuntimeState).toBe('idle');
    expect(next.Outline[0].CoreEvent).toBe('完整核心事件');
    expect(next.Outline[0].Hook).toBe('完整钩子');
    expect(next.Outline[0].Scenes).toEqual(['场景一', '场景二']);
    expect(next.Outline[0].WordBudget.TargetWords).toBe(3030);
  });

  it('preserves full Foundation review details across compact SSE snapshots', () => {
    const previous = {
      PlanningReview: { Kind: 'foundation', Status: 'pending' },
      PremiseFull: '完整设定',
      CharacterDetails: [{ id: 'lead', name: '主角' }],
      WorldRules: [{ id: 'rule', rule: '规则' }],
      PlannedRelationships: [{ id: 'rel', source_character_id: 'lead', target_character_id: 'support' }],
      CoreCharacterIDs: ['lead']
    };
    const incoming = {
      RuntimeState: 'idle',
      PlanningReview: { Kind: 'foundation', Status: 'pending' },
      PremiseFull: '',
      CharacterDetails: [],
      WorldRules: [],
      PlannedRelationships: [],
      CoreCharacterIDs: []
    };

    const next = preservePlanningReviewDetails(previous, incoming);

    expect(next.RuntimeState).toBe('idle');
    expect(next.PremiseFull).toBe('完整设定');
    expect(next.CharacterDetails).toEqual(previous.CharacterDetails);
    expect(next.WorldRules).toEqual(previous.WorldRules);
    expect(next.PlannedRelationships).toEqual(previous.PlannedRelationships);
    expect(next.CoreCharacterIDs).toEqual(['lead']);
  });

  it('allows Foundation details to clear after confirmation', () => {
    const previous = {
      PlanningReview: { Kind: 'foundation', Status: 'pending' },
      CharacterDetails: [{ id: 'lead' }],
      WorldRules: [{ id: 'rule' }]
    };
    const incoming = {
      PlanningReview: { Kind: 'volume_split', Status: 'collecting' },
      CharacterDetails: [],
      WorldRules: []
    };

    expect(preservePlanningReviewDetails(previous, incoming)).toBe(incoming);
  });

  it('does not drop workflow progress across consecutive snapshot events', () => {
    const workflowProgress = {
      workflow: 'normal',
      status: 'running',
      steps: [{ id: 'writing', label: '正文创作', status: 'running' }]
    };
    const withProgress = reduceWebEvent(createWorkbenchState(), {
      seq: 1,
      type: 'snapshot',
      snapshot: { completed_count: 3 },
      workflow_progress: workflowProgress
    });
    const legacyOnly = reduceWebEvent(withProgress, {
      seq: 2,
      type: 'snapshot',
      snapshot: { completed_count: 4 }
    });

    expect(legacyOnly.snapshot.completed_count).toBe(4);
    expect(legacyOnly.snapshot.workflow_progress).toBe(workflowProgress);
  });

  it('replays event history without duplicating stale events', () => {
    const initial = reduceWebEvent(createWorkbenchState(), {
      seq: 4,
      type: 'host_event',
      host_event_id: 'tool-1',
      event: { id: 'tool-1', summary: 'drafting', running: true }
    });
    const restored = reduceWebEvents(initial, [
      {
        seq: 4,
        type: 'host_event',
        host_event_id: 'tool-1',
        event: { id: 'tool-1', summary: 'stale duplicate', running: true }
      },
      {
        seq: 5,
        type: 'host_event',
        host_event_id: 'tool-1',
        event: { id: 'tool-1', summary: 'drafted', running: false }
      },
      {
        seq: 6,
        type: 'host_event',
        host_event_id: 'tool-2',
        event: { id: 'tool-2', summary: 'reviewing', running: true }
      }
    ]);

    expect(restored.lastSeq).toBe(6);
    expect(restored.eventRows).toHaveLength(2);
    expect(restored.eventRows[0].event.summary).toBe('drafted');
    expect(restored.eventRows[1].event.summary).toBe('reviewing');
  });

  it('keeps stream clear and delta rows stable', () => {
    const one = appendStreamDelta([{ id: 'round-0', text: '' }], '第一段');
    const cleared = startStreamRound(one);
    const two = appendStreamDelta(cleared, '第二段');

    expect(two).toEqual([
      { id: 'round-0', text: '第一段' },
      { id: 'round-1', text: '第二段' }
    ]);
  });

  it('collapses repeated draft stream rounds after refresh', () => {
    const first = '雨水敲打通风管，霓虹在远处闪烁。'.repeat(12);
    const second = `${first}他终于拔出接口线，继续向节点深处移动。`;
    const rounds = compactStreamRounds([
      { id: 'round-0', text: first },
      { id: 'round-1', text: second }
    ]);

    expect(rounds).toHaveLength(1);
    expect(rounds[0].text).toBe(second);
  });
});
