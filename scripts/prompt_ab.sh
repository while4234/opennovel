#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat >&2 <<'EOF'
Usage:
  scripts/prompt_ab.sh --prompt-file PROMPT --config CONFIG --variant-prompts DIR [--out DIR] [--max-chapters N]

Runs two isolated headless sessions:
  baseline: current embedded prompts
  variant: current source with files from DIR copied over assets/prompts/

DIR should contain one or more prompt files named like writer.md or architect-long.md.
EOF
}

prompt_file=""
config_file=""
variant_prompts=""
out_dir=""
max_chapters=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --prompt-file)
      [[ $# -ge 2 ]] || { usage; exit 2; }
      prompt_file="$2"
      shift 2
      ;;
    --config)
      [[ $# -ge 2 ]] || { usage; exit 2; }
      config_file="$2"
      shift 2
      ;;
    --variant-prompts)
      [[ $# -ge 2 ]] || { usage; exit 2; }
      variant_prompts="$2"
      shift 2
      ;;
    --out)
      [[ $# -ge 2 ]] || { usage; exit 2; }
      out_dir="$2"
      shift 2
      ;;
    --max-chapters)
      [[ $# -ge 2 ]] || { usage; exit 2; }
      max_chapters="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      usage
      exit 2
      ;;
  esac
done

[[ -n "$prompt_file" && -n "$config_file" && -n "$variant_prompts" ]] || { usage; exit 2; }
[[ -f "$prompt_file" ]] || { echo "prompt file not found: $prompt_file" >&2; exit 1; }
[[ -f "$config_file" ]] || { echo "config file not found: $config_file" >&2; exit 1; }
[[ -d "$variant_prompts" ]] || { echo "variant prompt dir not found: $variant_prompts" >&2; exit 1; }
[[ "$max_chapters" =~ ^[0-9]+$ ]] || { echo "--max-chapters must be a non-negative integer" >&2; exit 2; }

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$script_dir/.." && pwd)"
[[ -d "$root/assets/prompts" && -d "$root/cmd/ainovel-cli" ]] || {
  echo "cannot locate ainovel-cli repository root from $script_dir" >&2
  exit 1
}
prompt_abs="$(cd "$(dirname "$prompt_file")" && pwd)/$(basename "$prompt_file")"
config_abs="$(cd "$(dirname "$config_file")" && pwd)/$(basename "$config_file")"
variant_abs="$(cd "$variant_prompts" && pwd)"

if [[ -z "$out_dir" ]]; then
  out_dir="$root/workspace/prompt-ab/$(date +%Y%m%d-%H%M%S)"
fi
mkdir -p "$out_dir"
out_abs="$(cd "$out_dir" && pwd)"

copy_source() {
  local dest="$1"
  mkdir -p "$dest"
  rsync -a \
    --exclude .git \
    --exclude output \
    --exclude 'output*' \
    --exclude workspace \
    "$root/" "$dest/"
}

run_case() {
  local name="$1"
  local src="$2"
  local log="$out_abs/$name.log"
  local work="$out_abs/$name"

  echo "==> build $name" >&2
  (cd "$src" && GOWORK=off go build -o "$out_abs/ainovel-$name" ./cmd/ainovel-cli)

  echo "==> run $name" >&2
  mkdir -p "$work"
  if [[ "$max_chapters" -eq 0 ]]; then
    (
      cd "$work"
      exec "$out_abs/ainovel-$name" --config "$config_abs" --headless --prompt-file "$prompt_abs"
    ) >"$log" 2>&1
    return
  fi

  (
    cd "$work"
    exec "$out_abs/ainovel-$name" --config "$config_abs" --headless --prompt-file "$prompt_abs"
  ) >"$log" 2>&1 &
  local pid=$!
  local chapter_path
  chapter_path="$(printf "%s/output/novel/chapters/%02d.md" "$work" "$max_chapters")"
  while kill -0 "$pid" 2>/dev/null; do
    if [[ -s "$chapter_path" ]]; then
      echo "==> stop $name after chapter $max_chapters" >&2
      kill -TERM "$pid" 2>/dev/null || true
      for _ in {1..10}; do
        if ! kill -0 "$pid" 2>/dev/null; then
          wait "$pid" || true
          return
        fi
        sleep 1
      done
      kill -KILL "$pid" 2>/dev/null || true
      wait "$pid" || true
      return
    fi
    sleep 2
  done
  wait "$pid"
}

baseline_src="$out_abs/src-baseline"
variant_src="$out_abs/src-variant"

copy_source "$baseline_src"
copy_source "$variant_src"

shopt -s nullglob
files=("$variant_abs"/*.md)
if [[ ${#files[@]} -eq 0 ]]; then
  echo "variant prompt dir has no .md files: $variant_abs" >&2
  exit 1
fi
for file in "${files[@]}"; do
  base="$(basename "$file")"
  target="$variant_src/assets/prompts/$base"
  [[ -f "$target" ]] || { echo "unknown prompt file for variant: $base" >&2; exit 1; }
  cp "$file" "$target"
done

run_case baseline "$baseline_src"
run_case variant "$variant_src"

"$root/scripts/prompt_ab_report.py" "$out_abs"

cat <<EOF
prompt A/B run finished:
  $out_abs/baseline/output/novel
  $out_abs/variant/output/novel
logs:
  $out_abs/baseline.log
  $out_abs/variant.log
reports:
  $out_abs/report.md
  $out_abs/report.json
EOF
