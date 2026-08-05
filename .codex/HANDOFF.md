# Project Handoff

Last updated: 2026-08-05 17:06 CST
Project root: D:\OpenNovel
Branch: main
Status: ready_for_review

## Current Goal
Redesign OpenNovel's phone UI around a single content surface, contextual top bar, three-item bottom navigation, and mutually exclusive drawers/task panels for iPhone 15 Pro Max.

## Current Position
Implementation and scoped validation are complete. The desktop three-column layout and backend/API contracts remain unchanged; LAN startup and connectivity were intentionally left out of scope.

## Recent Actions
- Added a phone-only workspace chrome with project access, connection state, project actions, and fixed Writing/Manuscript/Tools navigation.
- Grouped the 14 tools into a full-screen mobile tool center with separate detail tasks, back navigation, and explicit close actions.
- Made project, tools, manuscript directory, action sheet, and dialogs mutually exclusive and dismissible with backdrop or Escape.
- Added phone-specific single-scroll writing, sticky composer, foundation, manuscript, table, dialog, safe-area, and touch-target styles.
- Added iPhone 15 Pro Max portrait/landscape Playwright projects, a mobile browser fixture, component tests, and style contracts.
- Rebuilt the embedded frontend assets.

## Changed / Relevant Files
- `internal/entry/web/ui/src/App.jsx`: mobile navigation state and integration with existing business actions.
- `internal/entry/web/ui/src/components/MobileWorkspaceChrome.jsx`: phone chrome and grouped tool navigation.
- `internal/entry/web/ui/src/styles.css`: phone shell, content scrolling, composer, drawers, and dialog behavior.
- `internal/entry/web/ui/src/foundation/foundation.css`: single-column mobile foundation center.
- `internal/entry/web/ui/src/manuscript/manuscript.css`: mobile-first manuscript reader and controls.
- `internal/entry/web/ui/tests/browser/mobile-workspace.spec.js`: portrait/landscape layout and overlay interaction coverage.
- `internal/entry/web/static`: rebuilt assets embedded by the Go application.

## Validation
- `npm.cmd test` -> 51 files and 424 tests passed.
- `npm.cmd run build` -> passed; 1,797 modules transformed and embedded assets regenerated.
- `PLAYWRIGHT_MOBILE_CHANNEL=chrome npx playwright test tests/browser/mobile-workspace.spec.js --project=mobile --project=mobile-landscape` -> 3 passed, 1 intentional landscape interaction skip.
- Existing mobile manuscript/foundation browser scenarios -> 2 passed with the same Chrome fallback.
- Portrait screenshots at `430x932` were inspected for top/bottom navigation, composer, tools, close action, and overflow.
- `git diff --check` -> passed.

## Blockers / Risks
- Playwright defaults mobile projects to WebKit, but the WebKit browser package could not be installed on this corporate network before timeout. The same viewports passed against system Chrome; real WebKit/Safari remains a home-environment validation item.
- The production build still reports the existing main JavaScript chunk over 500 kB; this change did not attempt code splitting.

## Next Steps
1. Pull/update this commit on the home computer and run the default WebKit mobile Playwright projects.
2. Test LAN access on a real iPhone 15 Pro Max or Safari responsive mode.
3. Push the local commit after review.

## Notes For Next Session
- Phone navigation applies through `767px`; `768-1100px` deliberately retains the existing compact drawer layout.
- Set `PLAYWRIGHT_MOBILE_CHANNEL=chrome` only as a local fallback when WebKit is unavailable.
