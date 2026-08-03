#!/usr/bin/env python3
"""Aggregate the context benchmark without copying source prose into Git."""

from __future__ import annotations

import argparse
import csv
import json
import re
import statistics
from collections import defaultdict
from pathlib import Path


PROSE_STAGES = {"opening_draft", "mid_continuation", "arc_close_edit"}


def parse_json(text: str):
    text = text.strip()
    text = re.sub(r"^```(?:json)?\s*", "", text)
    text = re.sub(r"\s*```$", "", text)
    try:
        return json.loads(text)
    except json.JSONDecodeError:
        return None


def cjk_count(text: str) -> int:
    return len(re.findall(r"[\u3400-\u9fff]", text))


def visible_char_count(text: str) -> int:
    return len(re.sub(r"\s+", "", text))


def xml_content(text: str, tag: str) -> str:
    match = re.search(rf"<{tag}>\s*(.*?)\s*</{tag}>", text, re.S | re.I)
    return match.group(1) if match else ""


def strict_success(row: dict) -> tuple[bool, list[str]]:
    stage = row["case"]["stage"]
    response = row.get("response", "")
    failures: list[str] = list(row.get("validation_issues", []))
    parsed = parse_json(response) if row["case"].get("structured") else None

    if row.get("status") != "success":
        failures.append(row.get("status", "not_success"))
        return False, sorted(set(failures))
    if stage == "skeleton_planning" and isinstance(parsed, dict):
        arcs = parsed.get("arcs", [])
        if len(arcs) != 4:
            failures.append("not_four_arcs")
        if [arc.get("chapters") for arc in arcs] != ["1-10", "11-20", "21-30", "31-40"]:
            failures.append("chapter_ranges")
        for name in ("王廉", "段天狼", "方冲"):
            if name not in response:
                failures.append("missing_character_arc")
                break
    elif stage == "detail_outline" and isinstance(parsed, dict):
        chapters = parsed.get("chapters", [])
        if [chapter.get("chapter") for chapter in chapters] != list(range(11, 19)):
            failures.append("chapter_sequence")
        if any(len(chapter.get("beats", [])) < 4 for chapter in chapters):
            failures.append("too_few_beats")
    elif stage == "initial_cocreate_draft":
        for tag in ("reply", "draft", "ready", "suggestions"):
            if not re.search(rf"<{tag}>.*?</{tag}>", response, re.S | re.I):
                failures.append("strict_xml_protocol")
                break
        draft = xml_content(response, "draft")
        if not 1200 <= visible_char_count(draft) <= 2200:
            failures.append("draft_length")
        for heading in ("改编模式", "核心目标", "原作事实锚点", "人物与关系规则", "必须保留", "禁止事项", "质量验收"):
            if heading not in draft:
                failures.append("missing_draft_section")
                break
    elif stage in PROSE_STAGES:
        chars = visible_char_count(response)
        if not 2600 <= chars <= 3200:
            failures.append("prose_length")
        if stage != "opening_draft" and ("段天狼" not in response or "凌雪伤" not in response):
            failures.append("missing_relationship_anchors")
        if any(term in response for term in ("心跳加速", "怦然心动", "占有欲", "脸红心跳")):
            failures.append("romance_drift")
    elif stage == "source_analysis" and isinstance(parsed, dict):
        reports = parsed.get("reports", [])
        expected = row["case"].get("analysis_segments", 0)
        if len(reports) != expected:
            failures.append("report_count")
        markers = [report.get("segment") for report in reports if isinstance(report, dict)]
        if markers != [f"SEG-{index:03d}" for index in range(1, expected + 1)]:
            failures.append("report_order")
        if any(len(report.get("events", [])) > 4 for report in reports if isinstance(report, dict)):
            failures.append("too_many_events")
    return not failures, sorted(set(failures))


def mean(values):
    return round(statistics.mean(values), 2) if values else None


def aggregate(results_dir: Path):
    nested_results = results_dir / "results"
    if nested_results.is_dir():
        results_dir = nested_results
    groups = defaultdict(list)
    for model_dir in sorted(path for path in results_dir.iterdir() if path.is_dir()):
        for result_file in model_dir.glob("*.json"):
            row = json.loads(result_file.read_text(encoding="utf-8"))
            row["model_id"] = model_dir.name
            row["strict_success"], row["strict_failures"] = strict_success(row)
            groups[(model_dir.name, row["case"]["stage"], row["case"]["target_tokens"])].append(row)

    output = []
    for (model, stage, target), rows in sorted(groups.items()):
        completed = [row for row in rows if row.get("status") == "success"]
        usage = [row.get("usage") or {} for row in completed]
        failures = defaultdict(int)
        for row in rows:
            for failure in row["strict_failures"]:
                failures[failure] += 1
        output.append({
            "model": model,
            "stage": stage,
            "target_tokens": target,
            "attempts": len(rows),
            "completed": len(completed),
            "strict_successes": sum(row["strict_success"] for row in rows),
            "strict_success_rate": round(sum(row["strict_success"] for row in rows) / len(rows), 4),
            "mean_latency_seconds": mean([row.get("duration_millis", 0) / 1000 for row in completed]),
            "mean_actual_input_tokens": mean([item.get("input", 0) for item in usage]),
            "mean_output_tokens": mean([item.get("output", 0) for item in usage]),
            "mean_output_chars": mean([visible_char_count(row.get("response", "")) for row in completed]),
            "failure_counts": dict(sorted(failures.items())),
        })
    return output


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("results_dir", type=Path)
    parser.add_argument("--json", type=Path, required=True)
    parser.add_argument("--csv", type=Path, required=True)
    args = parser.parse_args()
    rows = aggregate(args.results_dir)
    if not rows:
        parser.error(f"no benchmark result JSON files found under {args.results_dir}")
    args.json.parent.mkdir(parents=True, exist_ok=True)
    args.json.write_text(json.dumps(rows, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    with args.csv.open("w", encoding="utf-8-sig", newline="") as handle:
        writer = csv.DictWriter(handle, fieldnames=[key for key in rows[0] if key != "failure_counts"] + ["failure_counts"])
        writer.writeheader()
        for row in rows:
            writer.writerow({**row, "failure_counts": json.dumps(row["failure_counts"], ensure_ascii=False, sort_keys=True)})


if __name__ == "__main__":
    main()
