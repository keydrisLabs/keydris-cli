#!/usr/bin/env python3
"""Render PR text from untrusted model metadata and the validated Git index."""

import argparse
import html
import json
from pathlib import Path
import subprocess
import sys
import unicodedata


def load_description(path):
    if path.is_symlink() or not path.is_file() or path.stat().st_size > 32768:
        raise ValueError("Expected a regular PR description JSON file of at most 32 KiB")
    data = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(data, dict) or set(data) != {"title", "summary", "reason", "changes", "risks"}:
        raise ValueError("PR description has missing or unknown fields")

    def check_text(value, limit, single_line=False):
        if not isinstance(value, str) or not value.strip() or len(value) > limit:
            raise ValueError("PR description has an empty, oversized, or non-text field")
        if any(unicodedata.category(c).startswith("C") and c not in "\n\t" for c in value):
            raise ValueError("PR description contains control characters")
        if single_line and ("\n" in value or "\t" in value):
            raise ValueError("PR title must be a single line")

    check_text(data["title"], 100, single_line=True)
    for key in ("summary", "reason", "risks"):
        check_text(data[key], 3000)
    if not isinstance(data["changes"], list) or not 1 <= len(data["changes"]) <= 12:
        raise ValueError("PR description needs 1–12 changes")
    for change in data["changes"]:
        check_text(change, 1000)
    return data


def render(data, mode, live_result, files, run_url):
    if mode not in ("review", "dead-code", "e2e"):
        raise ValueError("Unknown maintenance mode")
    if live_result != ("success" if mode == "e2e" else "skipped"):
        raise ValueError("Unexpected live validation result; refusing to claim success")
    prefix = "test(dsh)" if mode == "e2e" else "chore(dsh)"
    title = f"{prefix}: {data['title'].strip()}"
    body = [data["summary"].strip(), "", "## Why", "", data["reason"].strip(),
            "", "## Changes", ""]
    body.extend("- " + change.strip().replace("\n", "\n  ") for change in data["changes"])
    body.extend(["", "## Changed files", "", "<!-- Derived from the validated patch. -->"])
    # JSON quoting makes unusual filenames unambiguous; HTML escaping prevents
    # filenames from creating mentions, links, or Markdown structure.
    body.extend("- <code>" + html.escape(json.dumps(name, ensure_ascii=False)) + "</code>" for name in files)
    body.extend(["", "## Validation", "",
                 "- Passed in the agent job: `go build ./...`, `go vet ./...`, `go test ./...`, and the Python test suite.",
                 "- The proposed patch passed validation in a fresh checkout before publication."])
    if mode == "dead-code":
        body.append("- CLI cross-builds passed for Linux, macOS, and Windows on amd64 and arm64 (six targets).")
    if mode == "e2e":
        body.append("- The proposed catalog passed real Claude Code and Codex sessions in WSL2, including live resource cleanup.")
    else:
        body.append("- Live WSL2 tests were not run for this proposal.")
    body.extend(["", "## Limitations", "", data["risks"].strip(), "",
                 f"[Workflow run]({run_url}) · Task: `{mode}` · Report artifact: `dsh-report-{mode}`", "",
                 "Summary and rationale were written by DeepSeek; the publisher supplies the file list and validation status. Human review is required before merging.", ""])
    return title, "\n".join(body)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("description", type=Path)
    parser.add_argument("--mode", required=True, choices=("review", "dead-code", "e2e"))
    parser.add_argument("--live-result", required=True, choices=("success", "skipped"))
    parser.add_argument("--run-url", required=True)
    parser.add_argument("--output-dir", type=Path, required=True)
    args = parser.parse_args()
    try:
        data = load_description(args.description)
        raw = subprocess.run(
            ["git", "-c", "core.hooksPath=/dev/null", "diff", "--cached", "--name-only", "--no-renames", "-z"],
            check=True, capture_output=True,
        ).stdout
        files = [name.decode("utf-8") for name in raw.split(b"\0") if name]
        if not files:
            raise ValueError("Cannot describe an empty patch")
        title, body = render(data, args.mode, args.live_result, files, args.run_url)
        (args.output_dir / "dsh-pr-title.txt").write_text(title + "\n", encoding="utf-8")
        (args.output_dir / "dsh-pr-body.md").write_text(body, encoding="utf-8")
    except (ValueError, OSError, subprocess.CalledProcessError):
        # Avoid echoing untrusted model text or filenames into workflow commands.
        print("::error::Invalid or missing DSH PR description; no PR was published", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
