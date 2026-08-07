# Project Handoff

Last updated: 2026-08-07 20:49 CST
Project root: D:\opennovel
Branch: main
Status: blocked

## Current Goal
Deploy OpenNovel entirely on D drive and expose its Web UI to phones on the same Wi-Fi network.

## Current Position
OpenNovel is built and running from `D:\opennovel` on port 9999 with runtime data rooted at `D:\opennovel-runtime`. LAN binding is active; creation of the Windows Firewall rule is blocked because the current process is not elevated.

## Recent Actions
- Cloned `https://github.com/while4234/opennovel.git` directly into `D:\opennovel` at upstream commit `54a3a77`.
- Installed the locked Web UI dependencies with npm cache under `D:\opennovel\.cache\npm`.
- Built the embedded Web UI, `ainovel-cli.exe`, `expansion-auditor.exe`, and `manuscript-completion-auditor.exe`.
- Started `ainovel-cli.exe web --host 0.0.0.0 --port 9999 --runtime-root D:\opennovel-runtime`.
- Redirected the service profile/home to `D:\opennovel-runtime\profile`; no provider credentials have been configured.
- Added `D:\opennovel-runtime\start-opennovel-lan.cmd` for repeatable D-drive LAN startup.
- Attempted to add a local-subnet firewall rule; Windows returned Access Denied because elevation is required.
- Removed the obsolete legacy-port reference from current project memory and neutralized it in historical Git notes.

## Changed / Relevant Files
- `.codex/HANDOFF.md`: current deployment state and remaining firewall step.
- `GIT_NOTES.md`: deployment validation and risk summary.
- `D:\opennovel-runtime\start-opennovel-lan.cmd`: untracked deployment launcher outside the repository.
- `scripts/restart-web.ps1`: existing build/restart entrypoint used for deployment.

## Validation
- `npm.cmd ci` -> PASS; 157 packages installed, npm reported 4 high-severity audit findings in the locked dependency tree.
- `restart-web.cmd -Port 9999 -BindAddress 0.0.0.0 -RuntimeRoot D:\opennovel-runtime` -> PASS.
- `GET http://127.0.0.1:9999/api/runtime` -> PASS; runtime root is `D:\opennovel-runtime` and setup is required.
- `D:\opennovel-runtime\start-opennovel-lan.cmd` -> PASS; no-build restart completed successfully.
- `GET http://192.168.1.5:9999/` -> HTTP 200 from the host after launcher validation.
- Listener inspection -> PID 41388, executable `D:\opennovel\ainovel-cli.exe`, listening on port 9999.
- Project-memory search for the obsolete port literal -> zero matches in `.codex`, `GIT_NOTES.md`, `GIT_HISTORY.md`, and the current self-evolution project memory.

## Blockers / Risks
- Windows Firewall is enabled and WLAN is classified Public. An administrator must allow inbound TCP 9999 from `LocalSubnet` before a phone can reliably connect.
- Locked npm dependencies currently report 4 high-severity audit findings; no automatic dependency upgrades were applied.
- The model/provider setup wizard has not been completed, so novel generation cannot start until the user supplies their own provider configuration.

## Next Steps
1. In an elevated PowerShell, add the `OpenNovel LAN TCP 9999` inbound rule for `Private,Public` profiles with `RemoteAddress LocalSubnet`.
2. From a phone on the same Wi-Fi, open `http://192.168.1.5:9999` and complete the model setup wizard.
3. Review upstream dependency updates before changing the locked npm tree.

## Notes For Next Session
- Restart this deployment with `D:\opennovel-runtime\start-opennovel-lan.cmd` so runtime/profile paths remain on D drive.
