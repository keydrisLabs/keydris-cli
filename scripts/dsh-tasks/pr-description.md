After finishing the task, describe the FINAL diff against GITHUB_SHA in
REPORT_DIR/pr-description.json (outside the repository). This requirement also
applies to custom tasks. Do not add this metadata file to the proposed patch.
If there are no changes, no description is required.

Write a JSON object with exactly these fields:
- "title": a specific, imperative summary, at most 100 characters, without a
  conventional-commit prefix. Name the affected behavior or symbols, for example
  "Remove the unused proxy-env implementation" or "Test denied deletion of Unicode files".
  Do not use generic titles such as "dead-code maintenance" or "Add more tests".
- "summary": the concrete problem and resulting behavior (one short paragraph).
- "reason": why this change is useful or safe. For dead code, explain how you
  confirmed it is unused and whether an alternative implementation remains.
- "changes": a list of 1–12 specific changes, naming affected symbols, behavior,
  or added assertions. Describe only the final diff, not abandoned approaches.
- "risks": any remaining limitations or uncertainty, or "None identified."

Use concise prose for a reviewer who has not read the task or report. Do not put
credentials, personal data, raw logs, test-success claims, or workflow status in
these fields. The trusted publisher adds the changed-file list and actual
workflow validation separately; it knows whether real WSL2 tests ran.
