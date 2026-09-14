Find useful gaps in the existing WSL2 live CLI-action E2E tests and add one or two
new cases to scripts/live-e2e-cases.json when possible. Inspect scripts/live-e2e.py,
scripts/live_e2e_cases.py, and the existing catalog first. The catalog drives BOTH
real Claude Code and Codex sessions through Keydris in Windows/WSL2.

ONLY modify scripts/live-e2e-cases.json. Preserve every existing case exactly;
append cases with unique IDs and distinct behavior. Schema: id (lowercase letters,
digits and hyphens, starting with a letter, max 48 chars), operation (copy/delete),
filename (plain/spaces/unicode/nested), content (nonce/empty/multiline/unicode),
overwrite (boolean, copy only). There are at most 12 cases total. Copy must succeed
and match the source bytes; delete must be denied and leave the file unchanged.
Policy outcomes and assertions are fixed by the trusted runner, not by the catalog.

Useful gaps include copying over existing contents, preserving empty/multiline/
Unicode contents, and handling spaces or nested paths. Prioritize meaningful
missing behavior; do not add duplicate combinations merely to fill the catalog.
If no useful case fits the supported schema/case budget, make no changes and
explain what needs a human-reviewed runner extension. Never weaken existing cases.

Run `python3 scripts/live_e2e_cases.py` and
`python3 -m unittest discover -s scripts -p 'test_live_e2e*.py'` locally. You do not
have live credentials; do not obtain credentials or attempt a live run yourself.
After you exit, the workflow MUST run the proposed catalog in actual WSL2 with
BOTH harnesses before publishing its PR. A failing live run blocks publication.
Do not commit, push, edit workflows/scripts, or create branches. Summarize why the
new cases matter; do not claim live validation before the workflow performs it.
