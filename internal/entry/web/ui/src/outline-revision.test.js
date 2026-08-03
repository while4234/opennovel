import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

const appSource = readFileSync(new URL('./App.jsx', import.meta.url), 'utf8');

function extractSection(source, startSignature, endSignature) {
  const start = source.indexOf(startSignature);
  const end = source.indexOf(endSignature, start + startSignature.length);
  expect(start).toBeGreaterThanOrEqual(0);
  expect(end).toBeGreaterThan(start);
  return source.slice(start, end);
}

describe('chapter outline revision UI wiring', () => {
  it('keeps outline revision state independent and resets it on project switches', () => {
    const stateBody = extractSection(
      appSource,
      'function createOutlineRevisionState()',
      'function createCoCreatePlanningRevisionState()'
    );

    expect(stateBody).toContain("chapter: '1'");
    expect(stateBody).toContain("instruction: ''");
    expect(stateBody).toContain('active: false');
    expect(stateBody).toContain("status: 'idle'");
    expect(appSource).toContain('const [outlineRevision, setOutlineRevision] = useState(createOutlineRevisionState);');
    expect(appSource).toContain('setOutlineRevision(createOutlineRevisionState());');
  });

  it('submits outline changes and refreshes the workbench from the response snapshot', () => {
    const submitBody = extractSection(
      appSource,
      'const submitOutlineRevision = async () =>',
      'const refreshOutlineRevision = async () =>'
    );

    expect(submitBody).toContain('buildOutlineRevisionPayload(outlineRevision, snapshot)');
    expect(submitBody).toContain('reviseChapterOutline(projectId, payload.body)');
    expect(submitBody).toContain('snapshot: data?.snapshot || previous.snapshot');
    expect(submitBody).toContain('outlineRevisionSuccessMessage(data?.revision, payload.body.chapter)');
    expect(submitBody).toContain('isCurrentProject(projectId)');
  });

  it('shows every outline chapter, blocks running submissions, and previews outline details only', () => {
    const controlsBody = extractSection(
      appSource,
      'function OutlineChapterRevisionControls(',
      'function CompletedChapterRevisionControls('
    );
    const workspaceBody = extractSection(
      appSource,
      'function OutlineChapterRevisionWorkspace(',
      'function CompletedChapterRevisionWorkspace('
    );

    expect(controlsBody).toContain('outline.map((item) =>');
    expect(controlsBody).toContain("!busy && !running && instruction.trim()");
    expect(controlsBody).toContain('创作运行中，请先暂停后再提交章节细纲修改。');
    expect(controlsBody).toContain('刷新细纲');
    expect(workspaceBody).toContain('<ProposalChapterCard');
    expect(workspaceBody).not.toContain('getChapter(');
    expect(workspaceBody).not.toContain('content.content');
  });
});
