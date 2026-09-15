Audit the WHOLE keydris-cli repository for dead code, not just recent commits.
Inventory Go packages, platform-specific implementations, shell/Python scripts,
Node/npm packaging, build/release entry points, generated files, and examples.
Use the cross-platform Go deadcode reports under REPORT_DIR as candidates only;
they are not proof that a symbol can safely be removed.

Trace each candidate's references across the entire repository, tests, scripts,
workflow/build invocations, reflection, generated bindings, OS/build tags, and
CLI string dispatch. Preserve exported compatibility surfaces and generated
sources unless you can establish that removing them is safe on every supported
platform. Do not remove tests just because they are not production entry points.
Do not delete an OS-specific implementation because another OS cannot reach it.
If any usage or compatibility requirement is uncertain, leave it and explain why.

Make a focused cleanup only for provably unused code. Run go build ./..., go vet
./..., go test ./..., and relevant script/packaging checks for the files changed.
Cross-build the CLI for linux, windows, and darwin (amd64 and arm64), CGO_ENABLED=0.
If there is no safe cleanup, leave the checkout unchanged; do not manufacture work.

Do not modify .github/, Git configuration, symlinks, or submodules. Do not commit,
push, or create branches. In the final report identify each removed item, the
evidence it is unused, validation performed, and any uncertain candidates retained.
