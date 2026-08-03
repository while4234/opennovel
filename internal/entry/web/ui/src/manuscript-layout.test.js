import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

const appSource = readFileSync(new URL('./App.jsx', import.meta.url), 'utf8');
const workspaceSource = readFileSync(new URL('./manuscript/ManuscriptWorkspace.jsx', import.meta.url), 'utf8');
const manuscriptStyles = readFileSync(new URL('./manuscript/manuscript.css', import.meta.url), 'utf8');

describe('professional manuscript workspace layout', () => {
  it('keeps manuscript viewing in the center and portals controls into the right panel', () => {
    expect(appSource).toContain("const [centerView, setCenterView] = useState('writing')");
    expect(appSource).toContain("active={centerView === 'manuscript'}");
    expect(appSource).toContain('controlsTarget={manuscriptControlsTarget}');
    expect(appSource).toContain('className="manuscript-controls-host"');
    expect(workspaceSource).toContain('createPortal(controls, controlsTarget)');
    expect(workspaceSource).not.toContain('manuscript-workspace-toggle');
  });

  it('uses one central scroll surface and a readable prose measure', () => {
    expect(manuscriptStyles).toMatch(/\.manuscript-content-scroll[\s\S]*overflow:\s*auto/);
    expect(manuscriptStyles).toMatch(/\.manuscript-reading-surface[\s\S]*1120px/);
    expect(manuscriptStyles).toMatch(/\.manuscript-reader[\s\S]*920px/);
    expect(manuscriptStyles).not.toContain('max-width: 72ch');
    expect(manuscriptStyles).not.toContain('max-height: 78vh');
  });
});
