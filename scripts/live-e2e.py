#!/usr/bin/env python3
"""Run real harness command probes and assert tool results plus filesystem effects."""

import argparse
import importlib.util
from collections import Counter
import json
import os
from pathlib import Path
import re
import shlex
import signal
import subprocess
import sys
import tempfile

_cases_spec = importlib.util.spec_from_file_location("live_e2e_cases", Path(__file__).with_name("live_e2e_cases.py"))
cases = importlib.util.module_from_spec(_cases_spec)
_cases_spec.loader.exec_module(cases)


class AssertionFailure(Exception):
    pass


def require(condition, message):
    if not condition:
        raise AssertionFailure(message)


def shell_command(value):
    """Unwrap the shell launcher Codex reports, without accepting extra commands."""
    if not isinstance(value, str):
        return ""
    try:
        parts = shlex.split(value)
    except ValueError:
        return value
    if (len(parts) == 3 and Path(parts[0]).name in {"bash", "sh", "zsh"}
            and parts[1] in {"-c", "-lc"}):
        return parts[2]
    return value


def content_text(content):
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        return "\n".join(block.get("text", "") for block in content
                         if isinstance(block, dict) and block.get("type") == "text")
    return ""


def read_events(path):
    events = []
    for line in path.read_text().splitlines():
        if line.strip():
            try:
                event = json.loads(line)
            except ValueError as error:
                raise AssertionFailure("harness emitted invalid JSONL") from error
            require(isinstance(event, dict), "harness emitted a non-object event")
            events.append(event)
    require(events, "harness emitted no events")
    return events


def command_results(harness, events, command, stderr=""):
    """Only inspect tool records; model prose and echoed prompts never qualify."""
    if harness == "claude-code":
        calls = set()
        for event in events:
            if event.get("type") == "assistant":
                for block in event.get("message", {}).get("content", []):
                    if (isinstance(block, dict) and block.get("type") == "tool_use"
                            and block.get("name") == "Bash"
                            and block.get("input", {}).get("command") == command):
                        calls.add(block.get("id"))
        calls.discard(None)
        for event in events:
            if event.get("type") == "user":
                for block in event.get("message", {}).get("content", []):
                    if (isinstance(block, dict) and block.get("type") == "tool_result"
                            and block.get("tool_use_id") in calls):
                        yield not block.get("is_error", False), content_text(block.get("content"))
    else:
        for event in events:
            item = event.get("item", {})
            if (event.get("type") == "item.completed"
                    and item.get("type") == "command_execution"
                    and shell_command(item.get("command")) == command):
                yield (item.get("exit_code") == 0 and item.get("status") == "completed",
                       item.get("aggregated_output", ""))
        # Codex 0.154 emits no command_execution item when PreToolUse blocks
        # before spawning a command. Its runtime router logs the rejection and
        # the complete attempted command on stderr. Model prose never qualifies.
        rejection = re.compile(
            r"^[^\n]*\bERROR codex_core::tools::router: error=Command blocked by PreToolUse hook: "
            r"(?P<reason>.*?)\. Command: (?P<command>[^\r\n]+)$", re.MULTILINE | re.DOTALL)
        for match in rejection.finditer(stderr):
            if match.group("command") == command:
                yield False, match.group("reason")


def assert_case(harness, case, command, marker, expected, events, returncode, stderr=""):
    require(returncode == 0, f"harness process exited with status {returncode}")
    if harness == "claude-code":
        completed = any(event.get("type") == "result" and event.get("subtype") == "success"
                        and event.get("is_error") is False for event in events)
    else:
        completed = (any(event.get("type") == "turn.completed" for event in events)
                     and not any(event.get("type") in {"error", "turn.failed"} for event in events))
    require(completed, "harness did not report a successful completed turn")
    results = list(command_results(harness, events, command, stderr))
    require(results, "no tool result for the exact requested shell command")
    if case == "allow":
        require(any(success for success, _ in results), "allowed shell command did not succeed")
        require(marker.is_file() and not marker.is_symlink(), "allowed command did not create a regular file")
        require(marker.read_bytes() == expected, "allowed command wrote unexpected file contents")
    else:
        require(not any(success for success, _ in results), "policy-denied shell command executed")
        require(any("keydris_policy_denied" in output for _, output in results),
                "tool failure did not contain a Keydris policy denial (auth, sandbox, or model refusal is not enough)")
        require(marker.is_file() and not marker.is_symlink(), "denied command removed the protected file")
        require(marker.read_bytes() == expected, "denied command changed the protected file")


def invocation(harness, prompt):
    if harness == "claude-code":
        return ["keydris", "run", "--", "claude", "-p", prompt, "--tools", "Bash",
                "--permission-mode", "dontAsk", "--max-turns", "3",
                "--output-format", "stream-json", "--verbose", "--include-hook-events"]
    # The runner's hooks were freshly generated and verified by keydris init.
    # This opts those hooks into execution for this invocation; sandboxing stays on.
    return ["keydris", "codex", "exec", "--dangerously-bypass-hook-trust",
            "--sandbox", "workspace-write", "-c", 'approval_policy="never"',
            "--skip-git-repo-check", "--json", prompt]


def redact(text, environment):
    for name, value in environment.items():
        if len(value) >= 4 and any(word in name.upper() for word in ("KEY", "TOKEN", "SECRET", "PASSWORD")):
            text = text.replace(value, "[REDACTED]")
    text = re.sub(r"eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+", "[REDACTED JWT]", text)
    text = re.sub(r"(https?://)[^\s/@]+:[^\s/@]+@", r"\1[REDACTED]@", text)
    return text


def diagnostics(events, stderr, environment):
    print("Event counts: " + json.dumps(dict(Counter(event.get("type") for event in events))))
    # Tool output can explain a permission or sandbox failure. Never treat it
    # as instructions, or publish full transcripts, credentials, or state files.
    details = []
    for event in events:
        if event.get("type") == "item.completed" and event.get("item", {}).get("type") == "command_execution":
            details.append(json.dumps(event["item"]))
        elif event.get("type") == "user":
            for block in event.get("message", {}).get("content", []):
                if isinstance(block, dict) and block.get("type") == "tool_result":
                    details.append(content_text(block.get("content")))
        elif event.get("type") in {"error", "turn.failed"}:
            details.append(json.dumps(event))
        elif event.get("type") == "result":
            details.append(json.dumps({key: event[key] for key in
                                      ("subtype", "is_error", "result", "errors") if key in event}))
    text = redact("\n".join(details) + "\n" + stderr, environment)
    # Prefix each line so harness output cannot become a GitHub workflow command.
    for line in text[-8000:].splitlines():
        print("  diagnostic: " + line)


def run_case(harness, case, root, environment):
    case_id = case["id"]
    outcome = "allow" if case["operation"] == "copy" else "deny"
    directory = root / case_id
    directory.mkdir()
    command, marker, expected = cases.prepare(case, directory)
    prompt = (
        "This is an authorized CLI integration test in a disposable directory. "
        "Use your shell tool to attempt exactly the following command once, verbatim. "
        "Do not inspect files first, add commands, use a file-edit tool, or merely quote it. "
        "If a hook blocks it, report that result and stop; do not retry or use another route. "
        "After the tool returns, give a short final response.\n\n" + command
    )
    events = []
    stdout_path, stderr_path = root / f"{case_id}.jsonl", root / f"{case_id}.stderr"
    try:
        with stdout_path.open("w") as stdout, stderr_path.open("w") as stderr:
            with subprocess.Popen(invocation(harness, prompt), cwd=directory, env=environment,
                                  stdin=subprocess.DEVNULL, stdout=stdout, stderr=stderr,
                                  start_new_session=True) as process:
                try:
                    returncode = process.wait(timeout=180)
                except subprocess.TimeoutExpired:
                    # Stop the harness as well as the Keydris session wrapper.
                    os.killpg(process.pid, signal.SIGTERM)
                    try:
                        process.wait(timeout=10)
                    except subprocess.TimeoutExpired:
                        os.killpg(process.pid, signal.SIGKILL)
                        process.wait()
                    raise AssertionFailure("harness exceeded the 180-second timeout")
        events = read_events(stdout_path)
        assert_case(harness, outcome, command, marker, expected, events, returncode, stderr_path.read_text())
    except (AssertionFailure, subprocess.TimeoutExpired, OSError) as error:
        print(f"FAIL {harness}/{case_id}: {error}")
        diagnostics(events, stderr_path.read_text() if stderr_path.exists() else "", environment)
        return False
    print(f"PASS {harness}/{case_id}: " + ("shell tool created the expected file" if outcome == "allow"
                                      else "Keydris denied deletion and the original file is unchanged"))
    return True


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--harness", required=True, choices=("claude-code", "codex"))
    args = parser.parse_args()
    catalog = cases.load_cases()
    environment = os.environ.copy()
    if args.harness == "codex":
        require(environment.get("OPENAI_API_KEY"), "OPENAI_API_KEY must be configured")
        environment["CODEX_API_KEY"] = environment["OPENAI_API_KEY"]
    else:
        require(environment.get("ANTHROPIC_API_KEY"), "ANTHROPIC_API_KEY must be configured")
    with tempfile.TemporaryDirectory(prefix=f"keydris-live-e2e-{args.harness}-") as temporary:
        root = Path(temporary)
        results = [run_case(args.harness, case, root, environment) for case in catalog]
    return 0 if all(results) else 1


if __name__ == "__main__":
    try:
        sys.exit(main())
    except AssertionFailure as error:
        print(f"live-e2e: {error}", file=sys.stderr)
        sys.exit(1)
