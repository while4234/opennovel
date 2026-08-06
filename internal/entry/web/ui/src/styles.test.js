import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

const css = readFileSync(new URL('./styles.css', import.meta.url), 'utf8');
const mobileChromeCss = readFileSync(new URL('./components/MobileWorkspaceChrome.css', import.meta.url), 'utf8');
const workspaceCss = readFileSync(new URL('./workspace/workspace.css', import.meta.url), 'utf8');
const indexHtml = readFileSync(new URL('../index.html', import.meta.url), 'utf8');

describe('ui styles', () => {
  it('uses a phone-only safe-area shell without changing the tablet drawer breakpoint', () => {
    expect(indexHtml).toContain('viewport-fit=cover');
    expect(css).toMatch(/@media \(max-width:\s*767px\)[\s\S]*?grid-template-rows:[\s\S]*?env\(safe-area-inset-top\)[\s\S]*?env\(safe-area-inset-bottom\)/);
    expect(css).toMatch(/@media \(max-width:\s*1100px\)[\s\S]*?\.mobile-workspace-nav\s*{/);
    expect(mobileChromeCss).toMatch(/@media \(max-width:\s*767px\)/);
    expect(mobileChromeCss).toContain('(max-height: 520px) and (max-width: 1024px)');
  });

  it('keeps the phone composer and navigation reachable with touch-sized controls', () => {
    expect(css).toMatch(/@media \(max-width:\s*767px\)[\s\S]*?\.composer\s*{[^}]*grid-template-columns:\s*minmax\(0,\s*1fr\)\s+auto;/);
    expect(css).toMatch(/\.icon-button,[\s\S]*?\.tool-button,[\s\S]*?min-height:\s*44px;/);
    expect(mobileChromeCss).toMatch(/\.mobile-phone-bottom-nav button\s*{[^}]*min-height:\s*50px;/s);
    expect(mobileChromeCss).toContain('padding: 5px max(12px, env(safe-area-inset-right)) max(5px, env(safe-area-inset-bottom)) max(12px, env(safe-area-inset-left))');
  });

  it('pins compact navigation to the viewport without covering the composer', () => {
    expect(mobileChromeCss).toMatch(/\.mobile-phone-bottom-nav\s*{[^}]*left:\s*0;[^}]*right:\s*0;[^}]*width:\s*100%;[^}]*grid-template-columns:\s*repeat\(3,\s*minmax\(0,\s*1fr\)\);/s);
    expect(workspaceCss).toMatch(/\.compatibility-workspace \.writing-pane\s*{[^}]*width:\s*100%;[^}]*max-width:\s*100vw;[^}]*min-width:\s*0;[^}]*padding:[^;]*var\(--mobile-bottom-nav-height\)/s);
    expect(workspaceCss).toMatch(/\.compatibility-workspace \.composer\s*{[^}]*width:\s*100%;[^}]*max-width:\s*100%;[^}]*min-width:\s*0;/s);
  });

  it('uses one phone workbench scroll surface and full-screen tool details', () => {
    expect(css).toMatch(/@media \(max-width:\s*767px\)[\s\S]*?\.workbench-stack\s*{[^}]*overflow-y:\s*auto;/);
    expect(css).toMatch(/\.stream-area\s*{[^}]*overflow:\s*visible;/s);
    expect(css).toMatch(/\.status-pane\s*{[^}]*width:\s*100%;/s);
    expect(css).toMatch(/\.status-pane\[data-mobile-view="detail"\][\s\S]*?grid-template-rows:\s*auto\s+minmax\(0,\s*1fr\);/);
  });

  it('keeps disabled library rows opaque during analysis', () => {
    expect(css).toMatch(/\.library-row\s*{[^}]*display:\s*grid;/s);
    expect(css).toMatch(/\.library-row:disabled\s*{[^}]*opacity:\s*1;/s);
  });

  it('keeps the workspace wider while giving the right tool pane room', () => {
    expect(css).toMatch(/grid-template-columns:\s*minmax\(224px,\s*264px\)\s*minmax\(520px,\s*1fr\)\s*minmax\(430px,\s*520px\);/);
  });

  it('keeps document scrolling locked to internal panes', () => {
    expect(css).toMatch(/html,\s*[\r\n]+body,\s*[\r\n]+#root\s*{[^}]*height:\s*100%;[^}]*overflow:\s*hidden;/s);
    expect(css).toMatch(/body\s*{[^}]*overscroll-behavior:\s*none;/s);
    expect(css).toMatch(/\.app-shell\s*{[^}]*height:\s*100dvh;[^}]*overflow:\s*hidden;/s);
    expect(css).not.toMatch(/\.app-shell\s*{[^}]*overflow:\s*auto;/s);
  });

  it('keeps side content inside the pane without horizontal scrolling', () => {
    expect(css).toMatch(/\.side-content\s*{[^}]*overflow-x:\s*hidden;/s);
    expect(css).toMatch(/\.simulation-section,\s*[\r\n]+\.cocreate-section\s*{[^}]*max-width:\s*100%;/s);
    expect(css).toMatch(/\.profile-status span\s*{[^}]*white-space:\s*nowrap;/s);
  });

  it('keeps trash actions visible while a long trash list scrolls independently', () => {
    expect(css).toMatch(/\.trash-panel\s*{[^}]*grid-template-rows:\s*auto\s+minmax\(0,\s*1fr\);[^}]*max-height:\s*min\(56vh,\s*520px\);/s);
    expect(css).toMatch(/\.trash-list\s*{[^}]*grid-template-rows:\s*minmax\(0,\s*1fr\)\s+auto;/s);
    expect(css).toMatch(/\.trash-items\s*{[^}]*overflow-y:\s*auto;[^}]*scrollbar-gutter:\s*stable;/s);
  });

  it('keeps adaptation audit findings readable instead of truncating their causal evidence', () => {
    expect(css).toMatch(/\.audit-finding-row strong,[\s\S]*?overflow-wrap:\s*anywhere;/s);
    expect(css).toMatch(/\.audit-confirmation-row\s*{[^}]*align-items:\s*flex-start;/s);
  });

  it('anchors hidden choice inputs so focusing them cannot scroll the page', () => {
    expect(css).toMatch(/\.target-option\s*{[^}]*position:\s*relative;[^}]*overflow:\s*hidden;/s);
    expect(css).toMatch(/\.target-option input\s*{[^}]*position:\s*absolute;[^}]*inset:\s*0;[^}]*margin:\s*0;/s);
    expect(css).toMatch(/\.adapt-mode\s*{[^}]*position:\s*relative;[^}]*overflow:\s*hidden;/s);
    expect(css).toMatch(/\.adapt-mode input\s*{[^}]*position:\s*absolute;[^}]*inset:\s*0;[^}]*margin:\s*0;/s);
    expect(css).toMatch(/\.side-content\s*{[^}]*overscroll-behavior:\s*contain;[^}]*scrollbar-gutter:\s*stable;/s);
  });

  it('uses a more compact desktop layout on short viewports', () => {
    expect(css).toMatch(/@media \(max-height:\s*900px\) and \(min-width:\s*981px\)\s*{/);
    expect(css).toMatch(/@media \(max-height:\s*900px\) and \(min-width:\s*981px\)\s*{[\s\S]*?\.workbench-stack\s*{[^}]*grid-template-rows:\s*minmax\(0,\s*1fr\)\s*minmax\(112px,\s*24%\);/);
    expect(css).toMatch(/@media \(max-height:\s*900px\) and \(min-width:\s*981px\)\s*{[\s\S]*?\.side-tabs button\s*{[^}]*min-height:\s*42px;/);
    expect(css).toMatch(/@media \(max-height:\s*900px\) and \(min-width:\s*981px\)\s*{[\s\S]*?\.adapt-brief,[\s\S]*?min-height:\s*72px;/);
  });

  it('keeps long co-create drafts from pushing action buttons away', () => {
    expect(css).toMatch(/\.draft-preview\s*{[^}]*overflow:\s*visible;/s);
    expect(css).not.toMatch(/(?:^|\n)\.draft-preview\s*{[^}]*max-height:/s);
    expect(css).toMatch(/\.cocreate-sticky-workspace \.draft-preview\s*{[^}]*max-height:\s*min\(34vh,\s*360px\);[^}]*overflow:\s*auto;/s);
  });

  it('keeps co-create waiting controls compact', () => {
    expect(css).toMatch(/\.cocreate-dialog\s*{[^}]*min-height:\s*280px;/s);
    expect(css).toMatch(/\.cocreate-dialog-suggestions\s*{[^}]*max-height:\s*132px;/s);
    expect(css).toMatch(/\.cocreate-side-suggestions\s*{[^}]*max-height:\s*176px;/s);
    expect(css).toMatch(/\.cocreate-workspace-output\s*{/);
    expect(css).toMatch(/\.cocreate-workspace-message\.assistant,\s*[\r\n]+\.cocreate-workspace-message\.thinking\s*{/);
    expect(css).toMatch(/\.cocreate-workspace-message\s*{[^}]*max-height:\s*min\(42vh,\s*380px\);[^}]*overflow:\s*hidden;/s);
    expect(css).toMatch(/\.cocreate-workspace-message pre\s*{[^}]*overflow:\s*auto;/s);
    expect(css).toMatch(/\.cocreate-workspace-bottom\s*{[^}]*min-height:\s*1px;/s);
    expect(css).toMatch(/\.cocreate-form textarea\s*{[^}]*max-height:\s*min\(24vh,\s*180px\);[^}]*overflow:\s*auto;/s);
    expect(css).toMatch(/\.cocreate-status-compact \.cocreate-actions\s*{[^}]*grid-template-columns:\s*1fr;/s);
  });

  it('shows co-create decision options vertically without truncating labels', () => {
    expect(css).toMatch(/\.decision-options\s*{[^}]*grid-template-columns:\s*minmax\(0,\s*1fr\);/s);
    expect(css).toMatch(/\.decision-options \.tool-button\s*{[^}]*display:\s*grid;[^}]*width:\s*100%;[^}]*white-space:\s*normal;/s);
    expect(css).toMatch(/\.decision-options \.tool-button\.recommended\s*{[^}]*border-color:\s*rgba\(15,\s*111,\s*109,\s*\.72\);[^}]*background:\s*var\(--accent-softer\);/s);
    expect(css).toMatch(/\.decision-options \.tool-button \.decision-option-letter\s*{[^}]*width:\s*26px;[^}]*min-width:\s*26px;/s);
    expect(css).toMatch(/\.decision-options \.tool-button span\s*{[^}]*overflow-wrap:\s*anywhere;[^}]*white-space:\s*normal;/s);
    expect(css).not.toMatch(/\.decision-options \.tool-button span\s*{[^}]*text-overflow:\s*ellipsis;/s);
  });

  it('shows a bounded co-create resume card after generation failures', () => {
    expect(css).toMatch(/\.cocreate-resume-card\s*{[^}]*display:\s*grid;[^}]*background:\s*var\(--warn-soft\);/s);
    expect(css).toMatch(/\.cocreate-resume-card span\s*{[^}]*overflow-wrap:\s*anywhere;/s);
  });

  it('keeps planning review revision controls visible and bounded', () => {
    expect(css).toMatch(/\.proposal-review-actions\s*{[^}]*display:\s*grid;[^}]*grid-template-columns:\s*repeat\(auto-fit,\s*minmax\(220px,\s*1fr\)\);/s);
    expect(css).toMatch(/\.planning-revision-controls\s*{[^}]*display:\s*grid;[^}]*min-width:\s*0;/s);
    expect(css).toMatch(/\.planning-revision-controls\.compact \.proposal-revision-textarea\s*{[^}]*min-height:\s*88px;/s);
  });

  it('keeps model management and add controls single-column full width', () => {
    expect(css).toMatch(/\.custom-model-form\s*{[^}]*grid-template-columns:\s*minmax\(0,\s*1fr\);/s);
    expect(css).toMatch(/\.model-editor-actions,\s*[\r\n]+\.existing-model-actions\s*{[^}]*grid-template-columns:\s*minmax\(0,\s*1fr\);/s);
    expect(css).toMatch(/\.backend-picker-row\s*{[^}]*grid-template-columns:\s*minmax\(0,\s*1fr\);/s);
    expect(css).toMatch(/\.model-editor-actions \.tool-button,\s*[\r\n]+\.existing-model-actions \.tool-button\s*{[^}]*width:\s*100%;/s);
  });
});
