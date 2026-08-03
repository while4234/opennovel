import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import { buildStageModelRouteOptions } from './App.jsx';

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

describe('project model settings panel', () => {
  it('saves retry settings through the project endpoint when a project is active', () => {
    const body = extractFunctionBody(appSource, 'changeRetrySettings');

    expect(body).toContain('activeProject?.id');
    expect(body).toContain('setProjectRetrySettings(activeProject.id, modelAttempts, repairAttempts, budgetAttempts, auditAttempts)');
    expect(body).toContain('setGlobalRetrySettings(modelAttempts, repairAttempts, budgetAttempts, auditAttempts)');
    expect(body).toContain('adaptationOutlineAuditRetryMaxAttempts');
  });

  it('edits global stage defaults when no project is active', () => {
    const switchBody = extractFunctionBody(appSource, 'switchModelRoute');
    const inheritBody = extractFunctionBody(appSource, 'inheritModelRoute');
    const thinkingBody = extractFunctionBody(appSource, 'changeThinking');

    expect(switchBody).toContain('switchGlobalModel(role, provider, model)');
    expect(inheritBody).toContain('inheritGlobalModel(role)');
    expect(thinkingBody).toContain('setGlobalThinking(role, level)');
    expect(appSource).toContain('const stages = modelConfig?.stages || []');
    expect(appSource).toContain('当前未打开项目：这里保存全局阶段默认，新建项目会使用这些设置。');
  });

  it('keeps same-named stage models distinguishable by provider', () => {
    expect(buildStageModelRouteOptions([
      { name: 'deepseek-suifeng', models: ['deepseek-v4-pro'] },
      { name: 'deepseek-yuanyu-0', models: ['deepseek-v4-pro'] }
    ])).toEqual([
      {
        provider: 'deepseek-suifeng',
        model: 'deepseek-v4-pro',
        value: '["deepseek-suifeng","deepseek-v4-pro"]',
        label: 'deepseek-suifeng / deepseek-v4-pro'
      },
      {
        provider: 'deepseek-yuanyu-0',
        model: 'deepseek-v4-pro',
        value: '["deepseek-yuanyu-0","deepseek-v4-pro"]',
        label: 'deepseek-yuanyu-0 / deepseek-v4-pro'
      }
    ]);
    expect(appSource).toContain('<span>创作阶段模型</span>');
    expect(appSource).toContain('每个阶段可继承项目默认路由，或明确选择“后端 / 模型”组合。');
    expect(appSource).toContain('独立细纲生成与修订使用“详细提纲”模型');
    expect(appSource).toContain("onSwitch(route.role, target.provider, target.model)");
    expect(appSource).not.toContain('<span>Agent 高级路由</span>');
  });

  it('keeps internal Agent routes out of the user-facing model settings', () => {
    expect(appSource).not.toContain('aria-label="Agent model routes"');
    expect(appSource).not.toContain('className="model-route-editor"');
    expect(appSource).not.toContain('Agent · {route.fallback_role}');
    expect(appSource).not.toContain('value={customModel.role}');
    expect(appSource).toContain('新建只登记一个新的配置 ID，不会改变当前默认模型或任何创作阶段模型');
  });

  it('keeps novel settings and outlines out of the backend diagnostics panel', () => {
    const start = appSource.indexOf('function BackendPanel(');
    const end = appSource.indexOf('\nfunction ', start + 1);
    const panelSource = appSource.slice(start, end);

    expect(start).toBeGreaterThanOrEqual(0);
    expect(end).toBeGreaterThan(start);
    expect(panelSource).toContain('<span>最近调用</span>');
    expect(panelSource).not.toContain('snapshot');
    expect(panelSource).not.toContain('小说设定');
    expect(panelSource).not.toContain('章节大纲');
  });
});
