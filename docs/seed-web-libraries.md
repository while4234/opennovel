# Web Library Seed Helper

`scripts/seed-web-libraries.ps1` seeds the requested Web runtime libraries after the new library APIs are available.

- Simulation profiles are uploaded through the Web multipart API: `POST /api/libraries/simulation/upload`.
- Novel prepared packages are imported locally with `go run .\cmd\seed-web-libraries`, reusing `LibraryService.SaveNovelFromPreparedRoot`.
- `lhk` is excluded from all seed and verify paths.

## Seed Set

Simulation profiles are read from `D:\AINovel\novel\simulation\*.json`, with `lhk.json` skipped.

Novel library entries:

| Key | Title | Source type | Source path |
| --- | --- | --- | --- |
| `xfk` | 大学刑法课 | `adaptation_backup` | `D:\AINovel\package\xfk\output\novel\meta\adaptation` |
| `gaz` | 诡案组 | `project_dir` | `D:\AINovel\novel\gaz` |
| `jqmq_1` | 娇妻美妾任君尝 | `project_dir` | `D:\AINovel\novel\jqmq_1` |
| `mzdnh` | 梦中的女孩 | `project_dir` | `D:\AINovel\novel\mzdnh` |
| `nsgl` | 女神攻略 | `project_dir` | `D:\AINovel\novel\nsgl` |

## Commands

Preview without mutating libraries:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\seed-web-libraries.ps1 `
  -BaseUrl http://127.0.0.1:9898 `
  -RuntimeRoot C:\Users\Hi\.ainovel\novels-preview `
  -PlanOnly
```

Seed through the Web API and local novel seed command:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\seed-web-libraries.ps1 `
  -BaseUrl http://127.0.0.1:9898 `
  -RuntimeRoot C:\Users\Hi\.ainovel\novels-preview `
  -Apply
```

Verify existing state:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\seed-web-libraries.ps1 `
  -BaseUrl http://127.0.0.1:9898 `
  -RuntimeRoot C:\Users\Hi\.ainovel\novels-preview `
  -VerifyOnly
```

Use `-Force` to replace existing novel library entries before import. Simulation profile duplicates are still subject to the Web API duplicate-name guard; delete or rename existing profiles before forcing a different JSON into the same name.

## Acceptance Checks

After seeding:

- `GET /api/libraries/simulation` contains all non-`lhk` simulation JSON files from `D:\AINovel\novel\simulation`.
- `GET /api/libraries/novels` contains the five named novel entries above.
- Every seeded novel has `source/source.txt`.
- Every seeded novel `meta/adaptation/source_manifest.json` points `source_path` at that library-local `source/source.txt`.
- Loading a novel into a new Web project rewrites the project manifest to the project-local upload copy and can start adaptation without calling `/adapt/analyze`.
