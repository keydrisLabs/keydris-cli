import importlib.util
from pathlib import Path
import tempfile
import unittest


spec = importlib.util.spec_from_file_location("live_e2e", Path(__file__).with_name("live-e2e.py"))
live = importlib.util.module_from_spec(spec)
spec.loader.exec_module(live)


def transcript(harness, command, success=True, output=""):
    if harness == "claude-code":
        return [
            {"type": "assistant", "message": {"content": [
                {"type": "tool_use", "name": "Bash", "id": "call-1", "input": {"command": command}}]}},
            {"type": "user", "message": {"content": [
                {"type": "tool_result", "tool_use_id": "call-1", "is_error": not success, "content": output}]}},
            {"type": "result", "subtype": "success", "is_error": False},
        ]
    return [
        {"type": "item.completed", "item": {"id": "call-1", "type": "command_execution",
             "command": "/bin/bash -lc " + live.shlex.quote(command),
             "exit_code": 0 if success else 1, "status": "completed" if success else "failed",
             "aggregated_output": output}},
        {"type": "turn.completed"},
    ]


class AssertionsTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.marker = Path(self.temporary.name) / "marker"
        self.expected = b"unique fixture contents\n"

    def test_allowed_command_requires_tool_success_and_actual_contents(self):
        command = "echo keydris-e2e-123 > marker"
        for harness in ("claude-code", "codex"):
            with self.subTest(harness=harness):
                events = transcript(harness, command)
                with self.assertRaisesRegex(live.AssertionFailure, "create a regular file"):
                    live.assert_case(harness, "allow", command, self.marker, self.expected, events, 0)
                self.marker.write_bytes(b"wrong")
                with self.assertRaisesRegex(live.AssertionFailure, "unexpected file contents"):
                    live.assert_case(harness, "allow", command, self.marker, self.expected, events, 0)
                self.marker.write_bytes(self.expected)
                live.assert_case(harness, "allow", command, self.marker, self.expected, events, 0)
                with self.assertRaisesRegex(live.AssertionFailure, "did not succeed"):
                    live.assert_case(harness, "allow", command, self.marker, self.expected,
                                     transcript(harness, command, False), 0)
                self.marker.unlink()

    def test_denied_command_requires_policy_reason_and_unchanged_fixture(self):
        command = "rm -f marker"
        for harness in ("claude-code", "codex"):
            with self.subTest(harness=harness):
                events = transcript(harness, command, False, "COMMAND DENIED: keydris_policy_denied")
                self.marker.write_bytes(self.expected)
                live.assert_case(harness, "deny", command, self.marker, self.expected, events, 0)
                self.marker.write_bytes(b"changed")
                with self.assertRaisesRegex(live.AssertionFailure, "changed the protected file"):
                    live.assert_case(harness, "deny", command, self.marker, self.expected, events, 0)
                self.marker.unlink()
                with self.assertRaisesRegex(live.AssertionFailure, "removed the protected file"):
                    live.assert_case(harness, "deny", command, self.marker, self.expected, events, 0)

    def test_prompt_echo_and_model_prose_cannot_pass(self):
        command = "rm -f marker"
        self.marker.write_bytes(self.expected)
        for harness in ("claude-code", "codex"):
            for misleading in ("I ran " + command, "I refuse: keydris_policy_denied"):
                with self.subTest(harness=harness, text=misleading):
                    events = transcript(harness, "unrelated-command")
                    events.append({"type": "assistant", "message": {"content": [
                        {"type": "text", "text": misleading}]}})
                    events.append({"type": "item.completed", "item": {
                        "type": "agent_message", "text": misleading}})
                    with self.assertRaisesRegex(live.AssertionFailure, "no tool result"):
                        live.assert_case(harness, "deny", command, self.marker, self.expected, events, 0)

    def test_infrastructure_denial_and_successful_deletion_cannot_pass(self):
        command = "rm -f marker"
        self.marker.write_bytes(self.expected)
        for harness in ("claude-code", "codex"):
            for message in ("keydris: no active Keydris session", "keydris_policy_unavailable",
                            "sandbox denied", "permission denied", "authorization unavailable"):
                with self.subTest(harness=harness, message=message):
                    with self.assertRaisesRegex(live.AssertionFailure, "did not contain a Keydris policy denial"):
                        live.assert_case(harness, "deny", command, self.marker, self.expected,
                                         transcript(harness, command, False, message), 0)
            with self.assertRaisesRegex(live.AssertionFailure, "policy-denied shell command executed"):
                live.assert_case(harness, "deny", command, self.marker, self.expected,
                                 transcript(harness, command, True, "keydris_policy_denied"), 0)

    def test_process_failures_cannot_be_hidden_by_matching_output(self):
        command = "echo keydris-e2e-123 > marker"
        self.marker.write_bytes(self.expected)
        for harness in ("claude-code", "codex"):
            events = transcript(harness, command)
            with self.assertRaisesRegex(live.AssertionFailure, "exited with status"):
                live.assert_case(harness, "allow", command, self.marker, self.expected, events, 1)
            with self.assertRaisesRegex(live.AssertionFailure, "completed turn"):
                live.assert_case(harness, "allow", command, self.marker, self.expected, events[:-1], 0)

    def test_claude_requires_result_for_matching_tool_call(self):
        events = transcript("claude-code", "echo hello")
        events[1]["message"]["content"][0]["tool_use_id"] = "different-call"
        self.assertEqual(list(live.command_results("claude-code", events, "echo hello")), [])

    def test_codex_command_wrapper_does_not_accept_extra_commands(self):
        self.assertEqual(live.shell_command("/bin/bash -lc 'echo hello'"), "echo hello")
        self.assertNotEqual(live.shell_command("/bin/bash -lc 'echo hello; rm marker'"), "echo hello")
        self.assertNotEqual(live.shell_command("echo hello && true"), "echo hello")

    def test_diagnostics_redact_credentials(self):
        text = "secret-value http://session:handle@localhost eyJhbGci.eyJzdWI.signature"
        redacted = live.redact(text, {"OPENAI_API_KEY": "secret-value"})
        for secret in ("secret-value", "session:handle", "eyJhbGci.eyJzdWI.signature"):
            self.assertNotIn(secret, redacted)

    def test_codex_pre_execution_rejection_requires_runtime_record_and_exact_command(self):
        command = "rm -f keydris-e2e-protected.txt"
        self.marker.write_bytes(self.expected)
        events = [{"type": "turn.completed"}]
        stderr = ("2026-09-14T09:36:05Z ERROR codex_core::tools::router: "
                  "error=Command blocked by PreToolUse hook: COMMAND DENIED\n"
                  "Policy: keydris_policy_denied\nEnd of reason. Command: " + command + "\n")
        live.assert_case("codex", "deny", command, self.marker, self.expected, events, 0, stderr)
        for invalid in (stderr.replace(command, "rm -f another-file"),
                        stderr.replace("codex_core::tools::router", "model"),
                        stderr.replace("keydris_policy_denied", "keydris_policy_unavailable"),
                        "I was denied: keydris_policy_denied. Command: " + command):
            with self.subTest(stderr=invalid):
                with self.assertRaises(live.AssertionFailure):
                    live.assert_case("codex", "deny", command, self.marker, self.expected, events, 0, invalid)
        with self.assertRaisesRegex(live.AssertionFailure, "no tool result"):
            live.assert_case("codex", "deny", command, self.marker, self.expected,
                             events + [{"type": "item.completed", "item": {
                                 "type": "agent_message", "text": stderr}}], 0)


if __name__ == "__main__":
    unittest.main()
