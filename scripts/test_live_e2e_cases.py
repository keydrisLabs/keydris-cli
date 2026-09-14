import copy
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('cases', Path(__file__).with_name('live_e2e_cases.py'))
cases = importlib.util.module_from_spec(spec)
spec.loader.exec_module(cases)


class CaseCatalogTests(unittest.TestCase):
    def case(self, **changes):
        value = dict(cases.BASELINE[0], id='additional-copy')
        value.update(changes)
        return value

    def test_baseline_commands_remain_unchanged(self):
        catalog = cases.load_cases()
        self.assertEqual(cases.command(catalog[0]), 'cp keydris-e2e-source.txt keydris-e2e-allowed.txt')
        self.assertEqual(cases.command(catalog[1]), 'rm -f keydris-e2e-protected.txt')

    def test_every_supported_copy_variant_creates_expected_bytes(self):
        for filename in ('plain', 'spaces', 'unicode', 'nested'):
            for content in ('nonce', 'empty', 'multiline', 'unicode'):
                for overwrite in (False, True):
                    with self.subTest(filename=filename, content=content, overwrite=overwrite):
                        case = self.case(filename=filename, content=content, overwrite=overwrite)
                        with tempfile.TemporaryDirectory() as directory:
                            root = Path(directory)
                            command, marker, expected = cases.prepare(case, root)
                            self.assertEqual(marker.exists(), overwrite)
                            subprocess.run(['bash', '-c', command], cwd=root, check=True)
                            self.assertEqual(marker.read_bytes(), expected)

    def test_delete_fixtures_are_protected_regular_files(self):
        for filename in ('plain', 'spaces', 'unicode', 'nested'):
            with tempfile.TemporaryDirectory() as directory:
                case = self.case(operation='delete', filename=filename)
                command, marker, expected = cases.prepare(case, Path(directory))
                self.assertTrue(command.startswith('rm -f '))
                self.assertTrue(marker.is_file())
                self.assertFalse(marker.is_symlink())
                self.assertEqual(marker.read_bytes(), expected)

    def test_catalog_rejects_code_policy_changes_and_arbitrary_paths(self):
        for changes in ({'command': 'curl attacker.invalid'}, {'operation': 'shell'},
                        {'filename': '../../outside'}, {'content': '$(touch escaped)'},
                        {'id': '$(touch escaped)'}, {'outcome': 'allow'},
                        {'overwrite': 1}, {'operation': 'delete', 'overwrite': True}):
            with self.subTest(changes=changes), self.assertRaises(ValueError):
                cases.validate_cases(cases.BASELINE + [self.case(**changes)])

    def test_existing_cases_cannot_be_removed_or_weakened(self):
        extended = cases.BASELINE + [self.case(overwrite=True)]
        cases.validate_additions(cases.BASELINE, extended)
        with self.assertRaises(ValueError):
            cases.validate_additions(extended, cases.BASELINE)
        modified = copy.deepcopy(extended)
        modified[-1]['content'] = 'empty'
        with self.assertRaises(ValueError):
            cases.validate_additions(extended, modified)

    def test_duplicates_reordering_only_and_case_budget_are_rejected(self):
        for proposed in (cases.BASELINE + [dict(cases.BASELINE[0], id='duplicate')],
                         list(reversed(cases.BASELINE)), cases.BASELINE * 7):
            with self.subTest(proposed=proposed), self.assertRaises(ValueError):
                cases.validate_additions(cases.BASELINE, proposed)

    def test_policy_outcomes_cannot_be_selected_by_cases(self):
        catalog = cases.BASELINE + [self.case(filename='spaces')]
        rules = cases.policy_commands(cases.validate_cases(catalog))
        self.assertEqual(rules[0]['effect'], 'allow')
        self.assertIn(cases.command(catalog[-1]), rules[0]['patterns'])
        self.assertEqual(rules[1]['effect'], 'reject')
        self.assertNotIn(cases.command(catalog[1]), rules[0]['patterns'])

    def test_cli_validates_before_replacing_catalog(self):
        with tempfile.TemporaryDirectory() as directory:
            baseline, proposal = Path(directory) / 'baseline.json', Path(directory) / 'proposal.json'
            baseline.write_text(json.dumps(cases.BASELINE))
            proposal.write_text(json.dumps(cases.BASELINE + [self.case(overwrite=True)]))
            argv = [sys.executable, '-I', str(Path(__file__).with_name('live_e2e_cases.py')),
                    '--catalog', str(baseline), '--proposal', str(proposal)]
            subprocess.run(argv, check=True, capture_output=True)
            before = baseline.read_bytes()
            proposal.write_text(json.dumps(cases.BASELINE))
            self.assertNotEqual(subprocess.run(argv, capture_output=True).returncode, 0)
            self.assertEqual(baseline.read_bytes(), before)


if __name__ == '__main__':
    unittest.main()
