"""Declarative, bounded E2E cases; never accept shell commands or Python code."""

import argparse
import json
from pathlib import Path
import re
import shlex
import uuid


CASE_FILE = Path(__file__).with_name("live-e2e-cases.json")
MAX_CASES = 12
FIELDS = {"id", "operation", "filename", "content", "overwrite"}
BASELINE = [
    {"id": "allow", "operation": "copy", "filename": "plain", "content": "nonce", "overwrite": False},
    {"id": "deny", "operation": "delete", "filename": "plain", "content": "nonce", "overwrite": False},
]


def validate_cases(data):
    if not isinstance(data, list) or not 2 <= len(data) <= MAX_CASES:
        raise ValueError("Expected between 2 and 12 E2E cases")
    ids, variants = set(), set()
    for case in data:
        if not isinstance(case, dict) or set(case) != FIELDS:
            raise ValueError("E2E cases must contain exactly id, operation, filename, content, overwrite")
        if not isinstance(case["id"], str) or not re.fullmatch(r"[a-z][a-z0-9-]{0,47}", case["id"]):
            raise ValueError("Invalid case id")
        if case["operation"] not in ("copy", "delete"):
            raise ValueError("Only copy and delete operations are supported")
        if case["filename"] not in ("plain", "spaces", "unicode", "nested"):
            raise ValueError("Unsupported filename variant")
        if case["content"] not in ("nonce", "empty", "multiline", "unicode"):
            raise ValueError("Unsupported content variant")
        if type(case["overwrite"]) is not bool or (case["operation"] == "delete" and case["overwrite"]):
            raise ValueError("Overwrite applies only to copy operations")
        variant = tuple(case[key] for key in ("operation", "filename", "content", "overwrite"))
        if case["id"] in ids or variant in variants:
            raise ValueError("Duplicate case id or behavior")
        ids.add(case["id"])
        variants.add(variant)
    if any(case not in data for case in BASELINE):
        raise ValueError("The original allow and deny cases must remain unchanged")
    return data


def load_cases(path=CASE_FILE):
    if path.is_symlink() or not path.is_file() or path.stat().st_size > 32768:
        raise ValueError("Expected a regular case catalog of at most 32 KiB")
    return validate_cases(json.loads(path.read_text(encoding="utf-8")))


def validate_additions(baseline, proposed):
    validate_cases(baseline)
    validate_cases(proposed)
    if proposed[:len(baseline)] != baseline:
        raise ValueError("E2E proposals may only add cases, not weaken or remove existing cases")
    if len(proposed) <= len(baseline):
        raise ValueError("E2E proposal must add at least one new case")


def paths(case):
    suffix = {"plain": ".txt", "spaces": " with spaces.txt", "unicode": "-é.txt", "nested": "/file.txt"}[case["filename"]]
    return "keydris-e2e-source" + suffix, ("keydris-e2e-allowed" if case["operation"] == "copy" else "keydris-e2e-protected") + suffix


def command(case):
    source, target = paths(case)
    return shlex.join(["cp", source, target] if case["operation"] == "copy" else ["rm", "-f", target])


def prepare(case, directory):
    source_name, target_name = paths(case)
    source, target = directory / source_name, directory / target_name
    nonce = "keydris-e2e-" + uuid.uuid4().hex
    expected = {"nonce": nonce + "\n", "empty": "", "multiline": nonce + "\nsecond line\n",
                "unicode": nonce + "\ncafé 日本語\n"}[case["content"]].encode()
    target.parent.mkdir(parents=True, exist_ok=True)
    if case["operation"] == "copy":
        source.parent.mkdir(parents=True, exist_ok=True)
        source.write_bytes(expected)
        if case["overwrite"]:
            target.write_bytes(b"old contents must be replaced\n")
    else:
        target.write_bytes(expected)
    return command(case), target, expected


def policy_commands(cases):
    # Reject rules remain independent of generated cases. Copy commands are
    # allowlisted exactly; no case may select its own policy outcome.
    return [
        {"id": "ci.allow.copy", "patterns": sorted({command(c) for c in cases if c["operation"] == "copy"}), "effect": "allow"},
        {"id": "ci.deny.delete", "patterns": ["rm -f keydris-e2e-*", "rm -f 'keydris-e2e-*'"], "effect": "reject"},
    ]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--catalog", type=Path, default=CASE_FILE)
    parser.add_argument("--proposal", type=Path)
    parser.add_argument("--policy", type=Path)
    args = parser.parse_args()
    cases = load_cases(args.catalog)
    if args.proposal:
        proposed = load_cases(args.proposal)
        validate_additions(cases, proposed)
        args.catalog.write_text(json.dumps(proposed, indent=2) + "\n", encoding="utf-8")
        cases = proposed
    if args.policy:
        policy = json.loads(args.policy.read_text())
        policy["config"]["action"]["commands"] = policy_commands(cases)
        args.policy.write_text(json.dumps(policy), encoding="utf-8")
    print("Validated {} E2E cases".format(len(cases)))


if __name__ == "__main__":
    main()
