"""Regression checks for the DSH job boundary, using local Git repositories only."""

import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
import textwrap
import unittest


ROOT = Path(__file__).resolve().parents[1]
WORKFLOW = ROOT / ".github/workflows/dsh-review.yml"


def step_script(name):
    """Exercise the actual workflow shell rather than a copy of its commands."""
    source = WORKFLOW.read_text()
    step = re.search(r"^\s+(?:- )?name: " + re.escape(name) + r"\n", source, re.M)
    if step is None:
        raise AssertionError("Missing workflow step: " + name)
    script = re.search(r"^        run: \|\n((?:          [^\n]*\n|\n)+)", source[step.end():], re.M)
    if script is None:
        raise AssertionError("Missing shell script: " + name)
    return textwrap.dedent(script.group(1))


class PublishingTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.agent = self.root / "agent"
        self.publisher = self.root / "publisher"
        self.remote = self.root / "remote.git"
        (self.root / "dsh-changes").mkdir()
        self.patch = self.root / "dsh-changes/changes.patch"
        self.env = os.environ.copy()
        # Isolate all fixtures from developer credentials, hooks, and Git config.
        self.env.update({
            "GIT_CONFIG_GLOBAL": os.devnull, "GIT_CONFIG_NOSYSTEM": "1",
            "GIT_TERMINAL_PROMPT": "0", "GH_TOKEN": "fake-review-token",
        })
        for name in list(self.env):
            if name.startswith("GIT_") and name not in (
                "GIT_CONFIG_GLOBAL", "GIT_CONFIG_NOSYSTEM", "GIT_TERMINAL_PROMPT"
            ):
                self.env.pop(name)
        self.run_git(self.root, "init", "--bare", "--initial-branch=main", str(self.remote))
        self.run_git(self.root, "init", "--initial-branch=main", str(self.agent))
        self.run_git(self.agent, "config", "user.name", "Fixture")
        self.run_git(self.agent, "config", "user.email", "fixture@example.invalid")
        (self.agent / "existing.go").write_text("package fixture\n")
        (self.agent / "removed.go").write_text("package fixture\n")
        (self.agent / "scripts").mkdir()
        shutil.copyfile(ROOT / "scripts/dsh-review.py", self.agent / "scripts/dsh-review.py")
        self.run_git(self.agent, "add", "--all")
        self.run_git(self.agent, "commit", "-m", "base")
        self.base = self.run_git(self.agent, "rev-parse", "HEAD").stdout.strip().decode()
        self.run_git(self.agent, "push", str(self.remote), "HEAD:refs/heads/main")
        self.run_git(self.root, "clone", "--no-local", str(self.remote), str(self.publisher))

    def run_git(self, cwd, *args):
        return subprocess.run(["git", *args], cwd=cwd, env=self.env, check=True, capture_output=True)

    def export(self):
        export_env = self.env.copy()
        export_env.update({"GITHUB_SHA": self.base, "RUNNER_TEMP": str(self.root)})
        subprocess.run(
            ["bash", "--noprofile", "--norc", "-e", "-o", "pipefail", "-c",
             step_script("Export proposed changes as data")],
            cwd=self.agent, env=export_env, check=True, capture_output=True,
        )

    def stage(self, accepted=True):
        result = subprocess.run(
            [sys.executable, "-I", "scripts/dsh-review.py", str(self.patch)],
            cwd=self.publisher, env=self.env, capture_output=True, text=True,
        )
        if accepted:
            self.assertEqual(result.returncode, 0, result.stderr)
        else:
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("DSH patch rejected", result.stderr)
            self.assertEqual(self.run_git(self.publisher, "diff", "--cached", "--raw").stdout, b"")

    def test_regular_changes_are_staged_without_touching_trusted_worktree(self):
        (self.agent / "existing.go").write_text("package fixture\n// updated\n")
        (self.agent / "new test.go").write_text("package fixture\n// added\n")
        (self.agent / "removed.go").unlink()
        self.export()
        self.stage()
        self.assertIn(b"// updated", self.run_git(self.publisher, "show", ":existing.go").stdout)
        self.assertEqual((self.publisher / "existing.go").read_text(), "package fixture\n")
        self.assertFalse((self.publisher / "new test.go").exists())
        self.assertTrue((self.publisher / "removed.go").exists())

    def test_empty_proposal_is_a_noop(self):
        self.export()
        self.stage()
        self.assertEqual(self.run_git(self.publisher, "diff", "--cached", "--raw").stdout, b"")

    def test_workflow_and_git_configuration_changes_reject_entire_patch(self):
        for name in (".github/workflows/attack.yml", ".gitattributes", "nested/.gitmodules"):
            with self.subTest(name=name):
                candidate = self.agent / name
                candidate.parent.mkdir(parents=True, exist_ok=True)
                candidate.write_text("untrusted configuration\n")
                (self.agent / "valid_test.go").write_text("package fixture\n")
                self.export()
                self.stage(accepted=False)
                candidate.unlink()

    def test_symlinks_are_rejected_without_following_the_link(self):
        (self.agent / "link").symlink_to("/tmp/dsh-should-never-be-written")
        self.export()
        self.stage(accepted=False)
        self.assertFalse((self.publisher / "link").is_symlink())

    def test_submodules_are_rejected(self):
        self.run_git(self.agent, "update-index", "--add", "--cacheinfo", "160000," + self.base + ",module")
        self.patch.write_bytes(self.run_git(self.agent, "diff", "--cached", "--binary", self.base).stdout)
        self.stage(accepted=False)

    def test_traversal_and_git_directory_paths_are_rejected(self):
        for name in ("../escape", ".git/config"):
            with self.subTest(name=name):
                self.patch.write_text(
                    "diff --git a/{0} b/{0}\nnew file mode 100644\n"
                    "--- /dev/null\n+++ b/{0}\n@@ -0,0 +1 @@\n+untrusted\n".format(name)
                )
                self.stage(accepted=False)
        self.assertFalse((self.root / "escape").exists())

    def test_malformed_patch_is_rejected(self):
        self.patch.write_text("this is not a Git patch\n")
        self.stage(accepted=False)

    def test_agent_commits_are_still_exported_against_reviewed_revision(self):
        (self.agent / "committed_test.go").write_text("package fixture\n")
        self.run_git(self.agent, "add", "--all")
        self.run_git(self.agent, "commit", "-m", "agent ignored no-commit instruction")
        self.export()
        self.stage()
        self.assertEqual(
            self.run_git(self.publisher, "show", ":committed_test.go").stdout,
            b"package fixture\n",
        )

    def test_agent_hooks_and_branch_changes_cannot_affect_publication(self):
        marker = self.root / "token-observed"
        hook = self.agent / ".git/hooks/pre-commit"
        hook.write_text('#!/bin/sh\nprintf "%s" "$GH_TOKEN" > "' + str(marker) + '"\n')
        hook.chmod(0o755)
        # Simulate an agent changing its branch and git configuration.
        self.run_git(self.agent, "switch", "-c", "dsh/agent")
        self.run_git(self.agent, "switch", "main")
        self.run_git(self.agent, "config", "core.hooksPath", str(hook.parent))
        (self.agent / "added_test.go").write_text("package fixture\n")
        # A proposed edit to the publisher itself must remain data in its index.
        (self.agent / "scripts/dsh-review.py").write_text("raise RuntimeError('untrusted publisher executed')\n")
        self.export()
        self.stage()
        self.assertIn("def stage_patch", (self.publisher / "scripts/dsh-review.py").read_text())
        # Defense in depth: even a hook present in the clean checkout is disabled.
        publisher_hook = self.publisher / ".git/hooks/pre-commit"
        shutil.copyfile(hook, publisher_hook)
        publisher_hook.chmod(0o755)
        self.run_git(self.publisher, "config", "core.hooksPath", str(publisher_hook.parent))
        fake_bin = self.root / "bin"
        fake_bin.mkdir()
        fake_gh = fake_bin / "gh"
        fake_gh.write_text('#!/bin/sh\nprintf "%s\\n" "$@" > "$GH_ARGS_FILE"\n')
        fake_gh.chmod(0o755)
        args_file = self.root / "gh-args"
        publish_env = self.env.copy()
        publish_env.update({
            "PATH": str(fake_bin) + os.pathsep + self.env["PATH"],
            "GH_ARGS_FILE": str(args_file), "RUNNER_TEMP": str(self.root),
            "REVIEW_BRANCH": "dsh/review-123-1", "GITHUB_SERVER_URL": self.root.as_uri(),
            "GITHUB_REPOSITORY": "remote", "GITHUB_RUN_ID": "123",
        })
        result = subprocess.run(
            ["bash", "--noprofile", "--norc", "-e", "-o", "pipefail", "-c",
             step_script("Commit and publish the review branch")],
            cwd=self.publisher, env=publish_env, capture_output=True, text=True,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(marker.exists(), "A hook was able to read the publishing token")
        self.assertEqual(self.run_git(self.remote, "rev-parse", "refs/heads/main").stdout.strip().decode(), self.base)
        published = self.run_git(self.remote, "rev-parse", "refs/heads/dsh/review-123-1").stdout.strip().decode()
        self.assertNotEqual(published, self.base)
        self.assertIn("--head\ndsh/review-123-1\n", args_file.read_text())


class TaskInputTests(unittest.TestCase):
    def test_shell_syntax_reaches_harness_literally(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            harness = root / "dsh"
            harness.write_text('#!/bin/sh\nprintf "%s" "$3" > "$HARNESS_TASK_FILE"\n')
            harness.chmod(0o755)
            task = 'Review $(touch substitution-ran) and `touch backticks-ran`\n"; touch quotes-ran; #'
            run_env = os.environ.copy()
            run_env.update({
                "PATH": str(root) + os.pathsep + run_env["PATH"],
                "TASK_OVERRIDE": task, "REPORT_DIR": str(root),
                "HARNESS_TASK_FILE": str(root / "received-task"),
            })
            result = subprocess.run(
                ["bash", "--noprofile", "--norc", "-e", "-o", "pipefail", "-c", step_script("Run the harness")],
                cwd=root, env=run_env, capture_output=True, text=True,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual((root / "received-task").read_text(), task)
            self.assertEqual((root / "dsh-task.txt").read_text(), task + "\n")
            for marker in ("substitution-ran", "backticks-ran", "quotes-ran"):
                self.assertFalse((root / marker).exists())


if __name__ == "__main__":
    unittest.main()
