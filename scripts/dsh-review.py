#!/usr/bin/env python3
"""Stage an untrusted DSH patch without writing or executing its contents.

Run only in the publishing job's fresh checkout of the trusted workflow SHA.
The agent's Git directory, configuration, hooks, and environment are not inputs.
"""

import argparse
from pathlib import Path
import subprocess
import sys


def git(*args):
    return subprocess.run(
        ["git", "-c", "core.hooksPath=/dev/null", *args],
        check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
    ).stdout


def stage_patch(patch):
    if git("diff", "--cached", "--raw"):
        raise ValueError("Publishing requires a clean index")
    if not patch.is_file() or patch.is_symlink():
        raise ValueError("Expected a regular changes.patch file")
    if patch.stat().st_size == 0:
        return

    try:
        # --cached never writes proposed files into the working tree. Do not use
        # --index/--unsafe-paths or check out the resulting index afterward.
        git("apply", "--cached", "--whitespace=nowarn", str(patch.resolve()))
        records = git("diff", "--cached", "--raw", "--no-renames", "-z").split(b"\0")
        for metadata, raw_path in zip(records[0:-1:2], records[1:-1:2]):
            old_mode, new_mode, *_ = metadata[1:].split()
            if old_mode not in (b"000000", b"100644", b"100755") or new_mode not in (
                b"000000", b"100644", b"100755"
            ):
                raise ValueError("Symlink and submodule changes are not permitted")
            name = raw_path.decode("utf-8", errors="strict")
            parts = name.casefold().split("/")
            if "\\" in name or any(part in (
                "", ".", "..", ".git", ".github", ".gitattributes", ".gitmodules"
            ) for part in parts):
                raise ValueError("Workflow, Git configuration, or unsafe path changes are not permitted")
    except (ValueError, subprocess.CalledProcessError):
        # Reject the whole proposal, leaving trusted working-tree files intact.
        git("reset", "--mixed", "--quiet", "HEAD")
        raise


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("patch", type=Path)
    args = parser.parse_args()
    try:
        stage_patch(args.patch)
    except (ValueError, OSError, subprocess.CalledProcessError) as error:
        # Do not print untrusted patch contents into workflow logs.
        print("::error::DSH patch rejected: {}".format(error), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
