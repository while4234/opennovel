#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""Generate deterministic metrics for prompt A/B runs.

The report intentionally does not score prose quality. It exposes measurable
facts so humans can compare output without guessing from raw logs.
"""

from __future__ import annotations

import argparse
import json
import re
from pathlib import Path
from typing import Any


def rune_count(text: str) -> int:
    return len(text)


def load_json(path: Path) -> Any:
    if not path.exists():
        return None
    return json.loads(path.read_text(encoding="utf-8"))


def count_lines(path: Path, pattern: str | None = None) -> int:
    if not path.exists():
        return 0
    n = 0
    regex = re.compile(pattern) if pattern else None
    with path.open("r", encoding="utf-8", errors="replace") as f:
        for line in f:
            if regex is None or regex.search(line):
                n += 1
    return n


def count_agent_tools(session_dir: Path) -> dict[str, int]:
    counts: dict[str, int] = {}
    if not session_dir.exists():
        return counts
    for file in sorted(session_dir.glob("*.jsonl")):
        agent = file.stem
        with file.open("r", encoding="utf-8", errors="replace") as f:
            for line in f:
                try:
                    item = json.loads(line)
                except json.JSONDecodeError:
                    continue
                tool = None
                if item.get("role") == "tool":
                    meta = item.get("metadata")
                    if isinstance(meta, dict):
                        tool = meta.get("tool_name")
                if not tool:
                    continue
                counts[f"{agent}:{tool}"] = counts.get(f"{agent}:{tool}", 0) + 1
    return counts


def tool_total(tools: dict[str, int], tool_name: str) -> int:
    suffix = f":{tool_name}"
    return sum(n for key, n in tools.items() if key.endswith(suffix))


def usage_overall(usage: dict[str, Any]) -> dict[str, Any]:
    overall = usage.get("overall")
    if isinstance(overall, dict):
        return overall
    return {}


def chapter_metrics(novel_dir: Path) -> list[dict[str, Any]]:
    chapters_dir = novel_dir / "chapters"
    if not chapters_dir.exists():
        return []
    out = []
    for file in sorted(chapters_dir.glob("*.md")):
        text = file.read_text(encoding="utf-8", errors="replace")
        out.append({
            "file": file.name,
            "runes": rune_count(text),
            "nonblank_lines": sum(1 for line in text.splitlines() if line.strip()),
        })
    return out


def summarize_case(run_root: Path, name: str) -> dict[str, Any]:
    work = run_root / name
    novel = work / "output" / "novel"
    log = run_root / f"{name}.log"
    progress = load_json(novel / "meta" / "progress.json") or {}
    usage = load_json(novel / "meta" / "usage.json") or {}
    chapters = chapter_metrics(novel)
    session_agents = novel / "meta" / "sessions" / "agents"
    tools = count_agent_tools(session_agents)
    overall = usage_overall(usage)

    return {
        "name": name,
        "novel_dir": str(novel),
        "log": str(log),
        "exists": novel.exists(),
        "phase": progress.get("phase"),
        "flow": progress.get("flow"),
        "current_chapter": progress.get("current_chapter"),
        "total_chapters": progress.get("total_chapters"),
        "completed_count": len(progress.get("completed_chapters") or []),
        "chapter_count": len(chapters),
        "chapter_runes": chapters,
        "total_runes": sum(ch["runes"] for ch in chapters),
        "assistant_messages": count_lines(log, r"\[DISPATCH\]|\[TOOL\]|\[SYSTEM\]"),
        "tool_calls": sum(tools.values()),
        "draft_events": tool_total(tools, "draft_chapter"),
        "commit_events": tool_total(tools, "commit_chapter"),
        "review_events": tool_total(tools, "save_review"),
        "repeat_warnings": count_lines(log, r"指令重复|repeat"),
        "errors": count_lines(log, r"ERROR|error:|失败|panic"),
        "cost_usd": overall.get("cost_usd"),
        "input_tokens": overall.get("input"),
        "output_tokens": overall.get("output"),
        "usage": usage,
        "tools": tools,
    }


def write_json(path: Path, payload: Any) -> None:
    path.write_text(json.dumps(payload, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def format_cell(value: Any) -> str:
    if value is None:
        return ""
    if isinstance(value, float):
        return f"{value:.6f}".rstrip("0").rstrip(".")
    return str(value)


def numeric_delta(baseline: Any, variant: Any) -> Any:
    if isinstance(baseline, (int, float)) and isinstance(variant, (int, float)):
        return variant - baseline
    return None


def format_delta(value: Any) -> str:
    if value is None:
        return ""
    if isinstance(value, float):
        text = f"{value:+.6f}".rstrip("0").rstrip(".")
        return "0" if text in {"+0", "-0"} else text
    if isinstance(value, int):
        return f"{value:+d}" if value else "0"
    return str(value)


def write_markdown(path: Path, report: dict[str, Any]) -> None:
    cases = report["cases"]
    lines = [
        "# Prompt A/B Report",
        "",
        f"- Run: `{report['run_root']}`",
        f"- Baseline: `{cases.get('baseline', {}).get('novel_dir', '')}`",
        f"- Variant: `{cases.get('variant', {}).get('novel_dir', '')}`",
        "",
        "| Metric | Baseline | Variant | Delta |",
        "| --- | ---: | ---: | ---: |",
    ]
    b = cases.get("baseline", {})
    v = cases.get("variant", {})
    metrics = [
        ("phase", "phase"),
        ("flow", "flow"),
        ("completed_count", "completed_count"),
        ("chapter_count", "chapter_count"),
        ("total_runes", "total_runes"),
        ("tool_calls", "tool_calls"),
        ("draft_events", "draft_events"),
        ("commit_events", "commit_events"),
        ("review_events", "review_events"),
        ("repeat_warnings", "repeat_warnings"),
        ("errors", "errors"),
        ("cost_usd", "cost_usd"),
        ("input_tokens", "input_tokens"),
        ("output_tokens", "output_tokens"),
    ]
    for label, key in metrics:
        delta = numeric_delta(b.get(key), v.get(key))
        lines.append(
            f"| {label} | {format_cell(b.get(key))} | {format_cell(v.get(key))} | {format_delta(delta)} |"
        )

    lines.extend(["", "## Chapters", ""])
    for name in ("baseline", "variant"):
        case = cases.get(name, {})
        lines.append(f"### {name}")
        chapters = case.get("chapter_runes") or []
        if not chapters:
            lines.append("")
            lines.append("No committed chapter files.")
            lines.append("")
            continue
        lines.append("")
        lines.append("| Chapter | Runes | Nonblank Lines |")
        lines.append("| --- | ---: | ---: |")
        for ch in chapters:
            lines.append(f"| {ch['file']} | {ch['runes']} | {ch['nonblank_lines']} |")
        lines.append("")

    lines.extend(["## Tool Calls", ""])
    for name in ("baseline", "variant"):
        case = cases.get(name, {})
        lines.append(f"### {name}")
        tools = case.get("tools") or {}
        if not tools:
            lines.append("")
            lines.append("No tool calls found in agent sessions.")
            lines.append("")
            continue
        lines.append("")
        lines.append("| Agent:Tool | Count |")
        lines.append("| --- | ---: |")
        for key in sorted(tools):
            lines.append(f"| `{key}` | {tools[key]} |")
        lines.append("")

    lines.extend([
        "## Notes",
        "",
        "- This report exposes deterministic run metrics only.",
        "- It does not decide which prose is better; read the produced chapters before accepting a prompt change.",
        "",
    ])
    path.write_text("\n".join(lines), encoding="utf-8")


def main() -> None:
    parser = argparse.ArgumentParser(description="Generate prompt A/B metrics report")
    parser.add_argument("run_root", help="A/B output directory containing baseline/ and variant/")
    parser.add_argument("--json", dest="json_path", help="JSON report path")
    parser.add_argument("--md", dest="md_path", help="Markdown report path")
    args = parser.parse_args()

    run_root = Path(args.run_root).resolve()
    report = {
        "run_root": str(run_root),
        "cases": {
            "baseline": summarize_case(run_root, "baseline"),
            "variant": summarize_case(run_root, "variant"),
        },
    }

    json_path = Path(args.json_path) if args.json_path else run_root / "report.json"
    md_path = Path(args.md_path) if args.md_path else run_root / "report.md"
    write_json(json_path, report)
    write_markdown(md_path, report)
    print(f"wrote {json_path}")
    print(f"wrote {md_path}")


if __name__ == "__main__":
    main()
