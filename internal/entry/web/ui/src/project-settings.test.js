import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import {
  buildProjectSimulationModeSaveRequest,
  buildProjectStyleSaveRequest,
  canSubmitProjectSimulationMode,
  canSubmitProjectStyle,
  isProjectStyleLocked,
  normalizeProjectStyleCatalog,
  normalizeSimulationMode,
  projectSimulationModeNotice,
  projectStyleLabel,
  resolveProjectSimulationMode,
  simulationModeLabel,
  resolveProjectStyleID,
  snapshotHasStartedWritingContent
} from './App.jsx';

const appSource = readFileSync(new URL('./App.jsx', import.meta.url), 'utf8');

function extractFunctionBody(source, name) {
  const signature = `const ${name} = async`;
  const signatureStart = source.indexOf(signature);
  expect(signatureStart).toBeGreaterThanOrEqual(0);

  const bodyStart = source.indexOf('{', signatureStart);
  expect(bodyStart).toBeGreaterThanOrEqual(0);

  let depth = 0;
  for (let index = bodyStart; index < source.length; index += 1) {
    const char = source[index];
    if (char === '{') {
      depth += 1;
    }
    if (char === '}') {
      depth -= 1;
      if (depth === 0) {
        return source.slice(bodyStart + 1, index);
      }
    }
  }

  throw new Error(`Could not find body for ${name}`);
}

describe('project settings panel', () => {
  it('adds the settings tab alongside project side tools', () => {
    expect(appSource).toContain("sideView === 'settings'");
    expect(appSource).toContain("setSideView('settings')");
    expect(appSource).toContain('role="tab" title="设定"');
  });

  it('uses style labels for display while preserving ids for values', () => {
    const catalog = normalizeProjectStyleCatalog({
      default_style: 'default',
      styles: [
        { id: 'default', label: '通用写作风格' },
        { id: 'fantasy', label: '奇幻冒险风格' }
      ]
    });

    expect(catalog.defaultStyle).toBe('default');
    expect(projectStyleLabel(catalog.styles, 'default')).toBe('通用写作风格');
    expect(projectStyleLabel(catalog.styles, 'fantasy')).toBe('奇幻冒险风格');
    expect(projectStyleLabel(catalog.styles, 'missing')).toBe('missing');
  });

  it('uses simulation mode labels and defaults to normal', () => {
    expect(normalizeSimulationMode('reinforced')).toBe('reinforced');
    expect(normalizeSimulationMode('unexpected')).toBe('normal');
    expect(simulationModeLabel('normal')).toBe('普通仿写');
    expect(simulationModeLabel('reinforced')).toBe('强化仿写');
    expect(appSource).toContain('仿写画像');
    expect(appSource).toContain('仿写模式');
    expect(appSource).toContain('强化仿写已降级生效');
    expect(appSource).toContain('使用更高预算的 Architect、Writer、Editor 共享契约');
    expect(appSource).toContain('不复制来源内容，也不承诺法律结论');
  });

  it('distinguishes pending and degraded reinforced activation for portable profiles', () => {
    const pending = projectSimulationModeNotice({
      currentMode: 'normal',
      selectedMode: 'reinforced',
      profile: { loaded: true, healthState: 'portable_only', localEvidence: false }
    });
    expect(pending.message).toContain('保存后将以降级强化模式生效');

    const degraded = projectSimulationModeNotice({
      currentMode: 'reinforced',
      selectedMode: 'reinforced',
      profile: {
        loaded: true,
        healthState: 'portable_only',
        localEvidence: false,
        selectedMode: 'reinforced',
        effectiveMode: 'reinforced',
        effectiveReason: 'portable_only',
        contract: { current: true, status: 'degraded' }
      }
    });
    expect(degraded.message).toContain('强化仿写已降级生效');
    expect(degraded.message).toContain('强化预算');
  });

  it('resolves current style from snapshot before runtime defaults', () => {
    const catalog = normalizeProjectStyleCatalog({
      default_style: 'default',
      styles: [{ id: 'romance', label: '言情风格' }]
    });

    expect(resolveProjectStyleID(
      { Style: 'romance' },
      { config: { style: 'fantasy' } },
      catalog
    )).toBe('romance');
  });

  it('resolves current simulation mode from snapshot, runtime, then normal', () => {
    expect(resolveProjectSimulationMode(
      { SimulationMode: 'reinforced' },
      { config: { simulation_mode: 'normal' } }
    )).toBe('reinforced');
    expect(resolveProjectSimulationMode(
      { simulation_mode: 'reinforced' },
      { config: { simulation_mode: 'normal' } }
    )).toBe('reinforced');
    expect(resolveProjectSimulationMode(
      {},
      { config: { simulation_mode: 'reinforced' } }
    )).toBe('reinforced');
    expect(resolveProjectSimulationMode({}, { config: {} })).toBe('normal');
  });

  it('builds save requests with the selected style id', () => {
    expect(buildProjectStyleSaveRequest(
      { id: 'project-1' },
      { selectedStyle: 'fantasy' }
    )).toEqual({
      ok: true,
      projectId: 'project-1',
      style: 'fantasy'
    });
  });

  it('builds simulation mode save requests with the selected mode', () => {
    expect(buildProjectSimulationModeSaveRequest(
      { id: 'project-1' },
      { selectedSimulationMode: 'reinforced' }
    )).toEqual({
      ok: true,
      projectId: 'project-1',
      mode: 'reinforced'
    });
  });

  it('allows style saves after proposal generation but before writing starts', () => {
    const projectSettings = {
      styles: [{ id: 'default', label: '通用写作风格' }, { id: 'romance', label: '言情风格' }],
      selectedStyle: 'romance',
      loadStatus: 'done',
      saveStatus: 'idle'
    };
    const snapshot = {
      NovelName: '半熟恋人',
      Phase: 'ready',
      TotalChapters: 36,
      CompletedCount: 0,
      TotalWordCount: 0,
      RuntimeState: 'idle',
      IsRunning: false
    };

    expect(snapshotHasStartedWritingContent(snapshot)).toBe(false);
    expect(canSubmitProjectStyle({
      activeProject: { id: 'project-1' },
      busy: false,
      currentStyle: 'default',
      projectSettings,
      snapshot
    })).toBe(true);
  });

  it('disables style saves after writing has started', () => {
    const projectSettings = {
      styles: [{ id: 'default', label: '通用写作风格' }, { id: 'romance', label: '言情风格' }],
      selectedStyle: 'romance',
      loadStatus: 'done',
      saveStatus: 'idle'
    };
    const snapshot = {
      TotalChapters: 12,
      CompletedCount: 1,
      TotalWordCount: 3200
    };

    expect(snapshotHasStartedWritingContent(snapshot)).toBe(true);
    expect(canSubmitProjectStyle({
      activeProject: { id: 'project-1' },
      busy: false,
      currentStyle: 'default',
      projectSettings,
      snapshot
    })).toBe(false);
  });

  it('treats a non-writing background task as temporary busy instead of a permanent style lock', () => {
    const projectSettings = {
      styles: [{ id: 'default', label: '通用写作风格' }, { id: 'romance', label: '言情风格' }],
      selectedStyle: 'romance',
      loadStatus: 'done',
      saveStatus: 'idle'
    };
    const snapshot = {
      CompletedCount: 0,
      TotalWordCount: 0,
      RuntimeState: 'running',
      IsRunning: true,
      Agents: [{ Name: 'web', State: 'running', TaskKind: 'simulation_import' }]
    };

    expect(isProjectStyleLocked(snapshot)).toBe(false);
    expect(canSubmitProjectStyle({
      activeProject: { id: 'project-1' },
      busy: false,
      currentStyle: 'default',
      projectSettings,
      snapshot
    })).toBe(false);
    expect(appSource).toContain('当前任务运行中，完成或暂停后可修改文风');
  });

  it('does not disable simulation mode saves because chapters are completed', () => {
    expect(canSubmitProjectSimulationMode({
      activeProject: { id: 'project-1' },
      busy: false,
      coCreateActive: false,
      currentSimulationMode: 'normal',
      projectSettings: {
        selectedSimulationMode: 'reinforced',
        simulationModeSaveStatus: 'idle'
      },
      snapshot: {
        CompletedCount: 3,
        TotalWordCount: 12000,
        RuntimeState: 'idle',
        IsRunning: false
      }
    })).toBe(true);
  });

  it('disables simulation mode saves when running or co-create is active', () => {
    const base = {
      activeProject: { id: 'project-1' },
      busy: false,
      coCreateActive: false,
      currentSimulationMode: 'normal',
      projectSettings: {
        selectedSimulationMode: 'reinforced',
        simulationModeSaveStatus: 'idle'
      }
    };

    expect(canSubmitProjectSimulationMode({
      ...base,
      snapshot: { IsRunning: true }
    })).toBe(false);
    expect(canSubmitProjectSimulationMode({
      ...base,
      coCreateActive: true,
      snapshot: { IsRunning: false }
    })).toBe(false);
  });

  it('saves through the project style endpoint and updates the active snapshot', () => {
    const body = extractFunctionBody(appSource, 'saveProjectStyle');

    expect(body).toContain('setProjectStyle(request.projectId, request.style)');
    expect(body).toContain('setActiveProject(data.project || activeProject)');
    expect(body).toContain('snapshot: data.snapshot || previous.snapshot');
  });

  it('saves simulation mode through the API helper and updates the active snapshot', () => {
    const body = extractFunctionBody(appSource, 'saveProjectSimulationMode');

    expect(body).toContain('setProjectSimulationMode(request.projectId, request.mode)');
    expect(body).toContain('setActiveProject(data.project || activeProject)');
    expect(body).toContain('snapshot: data.snapshot || previous.snapshot');
    expect(body).toContain("simulationModeMessage: '仿写模式已保存'");
  });
});
