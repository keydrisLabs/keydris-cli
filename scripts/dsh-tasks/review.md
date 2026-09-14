Review the last day of commits in keydris-cli (`git log --since="1 day ago" -p`).
Identify changed behavior missing test coverage and add focused Go tests using
the repository's existing style. Avoid unrelated changes.

Run `go build ./... && go vet ./... && go test ./...`; fix your regressions.
Do not modify .github/, Git configuration, symlinks, or submodules. Do not commit,
push, or create branches. Leave changes in the working tree and summarize the
changes, validation, and anything deliberately left unchanged.
