"""PR metadata is data; validation claims depend on the workflow, not the model."""

import copy
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("dsh_pr", Path(__file__).with_name("dsh_pr.py"))
pr = importlib.util.module_from_spec(spec)
spec.loader.exec_module(pr)


class DescriptionTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.path = Path(self.temp.name) / "pr-description.json"
        self.data = {
            "title": "Remove the unused proxy-env implementation",
            "summary": "Delete an obsolete proxy implementation with no callers.",
            "reason": "The proxyenv mode already uses NewSandboxProxy.",
            "changes": ["Remove NewProxyEnv and buildProxyEnvFlow.", "Update the README file listing."],
            "risks": "None identified.",
        }

    def load(self, data):
        self.path.write_text(json.dumps(data), encoding="utf-8")
        return pr.load_description(self.path)

    def test_specific_title_rationale_files_and_mode_validation(self):
        for mode in ("review", "dead-code", "e2e"):
            with self.subTest(mode=mode):
                title, body = pr.render(self.load(self.data), mode,
                                        "success" if mode == "e2e" else "skipped",
                                        ["README.md", "internal/node/dataplane/proxyenv.go"],
                                        "https://github.com/example/repo/actions/runs/123")
                self.assertIn(self.data["title"], title)
                self.assertIn(self.data["reason"], body)
                self.assertIn("internal/node/dataplane/proxyenv.go", body)
                self.assertIn("actions/runs/123", body)
                self.assertEqual("CLI cross-builds passed" in body, mode == "dead-code")
                self.assertEqual("passed real Claude Code and Codex" in body, mode == "e2e")
                self.assertEqual("Live WSL2 tests were not run" in body, mode != "e2e")

    def test_missing_malformed_oversized_and_symlink_metadata_rejected(self):
        with self.assertRaises(ValueError):
            pr.load_description(self.path)
        for content in ("{", "x" * 32769):
            self.path.write_text(content)
            with self.assertRaises(ValueError):
                pr.load_description(self.path)
        self.path.unlink()
        target = self.path.with_name("target.json")
        target.write_text(json.dumps(self.data))
        self.path.symlink_to(target)
        with self.assertRaises(ValueError):
            pr.load_description(self.path)

    def test_schema_and_control_characters_rejected(self):
        bad = [[], {}, dict(self.data, extra="field")]
        for key, value in [("title", ""), ("title", "a\nb"), ("title", "x" * 101),
                           ("title", "spoof\u202etext"), ("summary", "\x1b[1m"),
                           ("reason", 5), ("changes", []), ("changes", ["x"] * 13),
                           ("changes", [None]), ("risks", "x" * 3001)]:
            bad.append(dict(self.data, **{key: value}))
        for data in bad:
            with self.subTest(data=data):
                with self.assertRaises(ValueError):
                    self.load(data)

    def test_live_success_cannot_be_claimed_for_the_wrong_task(self):
        for mode, result in [("e2e", "skipped"), ("e2e", "failure"),
                             ("dead-code", "success"), ("review", "success")]:
            with self.subTest(mode=mode, result=result):
                with self.assertRaises(ValueError):
                    pr.render(self.data, mode, result, ["file.go"], "https://example.com")

    def test_shell_syntax_is_preserved_as_literal_data_and_paths_are_escaped(self):
        data = copy.deepcopy(self.data)
        data["title"] = 'Test $(touch nope) and `touch nope`'
        title, body = pr.render(self.load(data), "review", "skipped",
                                ['a\n</code><img src=x>@someone.md'], "https://example.com")
        self.assertIn(data["title"], title)
        self.assertIn("a\\n&lt;/code&gt;&lt;img src=x&gt;@someone.md", body)
        self.assertNotIn("<img", body)


if __name__ == "__main__":
    unittest.main()
