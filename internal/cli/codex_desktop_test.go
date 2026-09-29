package cli

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeAppServer answers the client's requests on the other end of a pipe. Each
// reply maps a request method to the lines sent back before the response.
func fakeAppServer(t *testing.T, replies map[string][]string) (io.Reader, io.Writer, <-chan []string) {
	t.Helper()
	clientOut, serverIn := io.Pipe()
	serverOut, clientIn := io.Pipe()
	methods := make(chan []string, 1)
	go func() {
		defer serverIn.Close()
		var seen []string
		lines := bufio.NewScanner(serverOut)
		for lines.Scan() {
			var request struct {
				Method string `json:"method"`
			}
			_ = json.Unmarshal(lines.Bytes(), &request)
			seen = append(seen, request.Method)
			for _, line := range replies[request.Method] {
				if _, err := io.WriteString(serverIn, line+"\n"); err != nil {
					break
				}
			}
			if request.Method == "hooks/list" {
				break
			}
		}
		methods <- seen
	}()
	return clientOut, clientIn, methods
}

func TestListCodexHooksSkipsNotificationsAndServerRequests(t *testing.T) {
	r, w, methods := fakeAppServer(t, map[string][]string{
		"initialize": {
			`{"method":"configWarning","params":{}}`,
			`{"id":1,"result":{"userAgent":"codex"}}`,
		},
		"hooks/list": {
			// A server request that reuses the client's id is not the response.
			`{"id":2,"method":"account/refresh","params":{}}`,
			`{"method":"hook/started","params":{}}`,
			`{"id":2,"result":{"data":[{"cwd":"/tmp","errors":[],"warnings":[],"hooks":[` +
				`{"key":"/<session-flags>/config.toml:pre_tool_use:0:0","eventName":"preToolUse","source":"sessionFlags","currentHash":"sha256:a","trustStatus":"untrusted"},` +
				`{"key":"/home/hooks.json:pre_tool_use:0:0","eventName":"preToolUse","source":"user","currentHash":"sha256:b","trustStatus":"trusted"}]}]}}`,
		},
	})
	hooks, err := listCodexHooks(r, w, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if got := <-methods; strings.Join(got, ",") != "initialize,initialized,hooks/list" {
		t.Fatalf("requests = %v", got)
	}
	ours := sessionFlagHooks(hooks)
	if len(hooks) != 2 || len(ours) != 1 || ours[0].CurrentHash != "sha256:a" || ours[0].EventName != "preToolUse" {
		t.Fatalf("hooks = %+v", hooks)
	}
}

func TestListCodexHooksReportsProtocolAndHookErrors(t *testing.T) {
	for name, response := range map[string]string{
		"request error": `{"id":2,"error":{"code":-32601,"message":"method not found"}}`,
		"hook error":    `{"id":2,"result":{"data":[{"cwd":"/tmp","errors":[{"message":"bad hooks table","path":"/x"}],"warnings":[],"hooks":[]}]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			r, w, _ := fakeAppServer(t, map[string][]string{
				"initialize": {`{"id":1,"result":{}}`},
				"hooks/list": {response},
			})
			if _, err := listCodexHooks(r, w, "/tmp"); err == nil {
				t.Fatal("error accepted")
			}
		})
	}
	r, w, _ := fakeAppServer(t, map[string][]string{"initialize": {`{"id":1,"result":{}}`}})
	w.(*io.PipeWriter).CloseWithError(io.ErrClosedPipe)
	if _, err := listCodexHooks(r, w, "/tmp"); err == nil {
		t.Fatal("a closed server was accepted")
	}
}

func TestCodexHookTrustOverrideIsOneSortedTable(t *testing.T) {
	got := codexHookTrustOverride([]codexListedHook{
		{Key: "/<session-flags>/config.toml:session_start:0:0", CurrentHash: "sha256:c"},
		{Key: "/<session-flags>/config.toml:pre_tool_use:0:0", CurrentHash: "sha256:a"},
	})
	want := `hooks.state={"/<session-flags>/config.toml:pre_tool_use:0:0"={trusted_hash="sha256:a",enabled=true},"/<session-flags>/config.toml:session_start:0:0"={trusted_hash="sha256:c",enabled=true}}`
	if got != want {
		t.Fatalf("override = %s\nwant       %s", got, want)
	}
}

func TestCodexDesktopOverridesRequireEveryHookTrusted(t *testing.T) {
	hook := func(event, trust string) codexListedHook {
		return codexListedHook{Key: "/<session-flags>/config.toml:" + event + ":0:0", EventName: event, Source: "sessionFlags", CurrentHash: "sha256:" + event, TrustStatus: trust, Enabled: true}
	}
	disabled := hook("permissionRequest", "trusted")
	disabled.Enabled = false
	user := codexListedHook{Key: "/home/hooks.json:pre_tool_use:0:0", Source: "user", TrustStatus: "untrusted"}
	cases := map[string]struct {
		first, second []codexListedHook
		ok            bool
	}{
		"trusted": {
			first:  []codexListedHook{hook("preToolUse", "untrusted"), hook("permissionRequest", "untrusted"), hook("sessionStart", "untrusted"), user},
			second: []codexListedHook{hook("preToolUse", "trusted"), hook("permissionRequest", "trusted"), hook("sessionStart", "trusted"), user},
			ok:     true,
		},
		"trust not applied": {
			first:  []codexListedHook{hook("preToolUse", "untrusted"), hook("permissionRequest", "untrusted"), hook("sessionStart", "untrusted")},
			second: []codexListedHook{hook("preToolUse", "trusted"), hook("permissionRequest", "modified"), hook("sessionStart", "trusted")},
		},
		"hook disabled": {
			first:  []codexListedHook{hook("preToolUse", "untrusted"), hook("permissionRequest", "untrusted"), hook("sessionStart", "untrusted")},
			second: []codexListedHook{hook("preToolUse", "trusted"), disabled, hook("sessionStart", "trusted")},
		},
		"hook not loaded": {
			first: []codexListedHook{hook("preToolUse", "untrusted"), hook("sessionStart", "untrusted")},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var calls [][]string
			previous := codexSessionFlagHooks
			codexSessionFlagHooks = func(_ string, overrides []string) ([]codexListedHook, error) {
				calls = append(calls, overrides)
				if len(calls) == 1 {
					return tc.first, nil
				}
				return tc.second, nil
			}
			t.Cleanup(func() { codexSessionFlagHooks = previous })

			overrides, err := codexDesktopOverrides("/app/codex", codexHooks("'/bin/keydris'"))
			if !tc.ok {
				if err == nil {
					t.Fatal("untrusted hooks accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(calls) != 2 || strings.Contains(strings.Join(calls[0], "\n"), "hooks.state=") {
				t.Fatalf("listings = %q", calls)
			}
			last := overrides[len(overrides)-1]
			if !strings.HasPrefix(last, "hooks.state={") || strings.Contains(last, "hooks.json") {
				t.Fatalf("trust override = %s", last)
			}
			if overrides[0] != "features.hooks=true" || len(overrides) != len(codexEnforcementOverrides())+4 {
				t.Fatalf("overrides = %q", overrides)
			}
		})
	}
}

func TestCodexDesktopShimJoinsTheCallersLastConfigGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the shim is a POSIX shell script")
	}
	dir := t.TempDir()
	fake := filepath.Join(dir, "codex app", "codex")
	if err := os.MkdirAll(filepath.Dir(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nfor arg in \"$@\"; do printf '%s\\n' \"$arg\"; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	overrides := []string{
		`hooks.PreToolUse=[{matcher="^Bash$",hooks=[{type="command",command="'/opt/key dris/keydris' __pretool-use --codex",timeout=30}]}]`,
		`features.network_proxy.domains={"*"="allow"}`,
	}
	shim := filepath.Join(dir, "data", "bin", "codex")
	if err := writeCodexDesktopShim(shim, fake, overrides); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(shim); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("shim mode = %v, %v", info, err)
	}
	ours := []string{"-c", overrides[0], "-c", overrides[1]}
	with := func(before []string, after ...string) []string {
		return append(append(append([]string{}, before...), ours...), after...)
	}
	for name, tc := range map[string]struct{ args, want []string }{
		"no -c": {
			[]string{"app-server"},
			with(nil, "app-server"),
		},
		"-c before the subcommand": {
			[]string{"-c", "features.code_mode_host=true", "app-server", "--analytics-default-enabled"},
			with([]string{"-c", "features.code_mode_host=true"}, "app-server", "--analytics-default-enabled"),
		},
		// Codex drops the -c group before the subcommand when one follows it.
		"-c after the subcommand": {
			[]string{"-c", "a=1", "app-server", "--analytics-default-enabled", "-c", "b=2"},
			with([]string{"-c", "a=1", "app-server", "--analytics-default-enabled", "-c", "b=2"}),
		},
		"attached values": {
			[]string{"--config=a=1", "app-server", "-cb=2", "--analytics-default-enabled"},
			with([]string{"--config=a=1", "app-server", "-cb=2"}, "--analytics-default-enabled"),
		},
		"command after --": {
			[]string{"sandbox", "macos", "--", "sh", "-c", "echo 'hi there'"},
			with(nil, "sandbox", "macos", "--", "sh", "-c", "echo 'hi there'"),
		},
		"-c before a command after --": {
			[]string{"-c", "a=1", "sandbox", "macos", "--", "sh", "-c", "echo 'hi there'"},
			with([]string{"-c", "a=1"}, "sandbox", "macos", "--", "sh", "-c", "echo 'hi there'"),
		},
	} {
		t.Run(name, func(t *testing.T) {
			out, err := exec.Command(shim, tc.args...).Output()
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n"); strings.Join(got, "\x00") != strings.Join(tc.want, "\x00") {
				t.Fatalf("arguments = %q\nwant        %q", got, tc.want)
			}
		})
	}
}

func TestVerifyCodexDesktopShimRunsEachAppInvocation(t *testing.T) {
	ready := []codexListedHook{
		{EventName: "preToolUse", Source: "sessionFlags", TrustStatus: "trusted", Enabled: true},
		{EventName: "permissionRequest", Source: "sessionFlags", TrustStatus: "trusted", Enabled: true},
		{EventName: "sessionStart", Source: "sessionFlags", TrustStatus: "trusted", Enabled: true},
	}
	var runs [][]string
	previous := codexShimHooks
	t.Cleanup(func() { codexShimHooks = previous })
	codexShimHooks = func(executable string, args []string) ([]codexListedHook, error) {
		if executable != "/data/bin/codex" {
			t.Fatalf("listed %s, want the shim", executable)
		}
		runs = append(runs, args)
		return ready, nil
	}
	if err := verifyCodexDesktopShim("/data/bin/codex", 3); err != nil {
		t.Fatal(err)
	}
	if len(runs) != len(codexDesktopInvocations) || runs[1][len(runs[1])-2] != "-c" {
		t.Fatalf("invocations = %q", runs)
	}

	// The app's plugin flags follow the subcommand; a shim that loses the
	// Keydris hooks there must stop the launch.
	codexShimHooks = func(_ string, args []string) ([]codexListedHook, error) {
		if args[len(args)-2] == "-c" {
			return nil, nil
		}
		return ready, nil
	}
	err := verifyCodexDesktopShim("/data/bin/codex", 3)
	if err == nil || !strings.Contains(err.Error(), "0 of 3") || !strings.Contains(err.Error(), "plugins.") {
		t.Fatalf("err = %v", err)
	}
}

func fakeCodexDesktopBundle(t *testing.T, codexPath ...string) string {
	t.Helper()
	app := filepath.Join(t.TempDir(), "ChatGPT.app")
	for _, path := range append([]string{"Contents/MacOS/ChatGPT"}, codexPath...) {
		full := filepath.Join(app, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("binary"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("KEYDRIS_CODEX_DESKTOP_APP", app)
	return app
}

func stubCodexDesktopBundleInfo(t *testing.T, id string) {
	t.Helper()
	previous := codexDesktopBundleInfo
	codexDesktopBundleInfo = func(string) (string, string, error) { return id, "ChatGPT", nil }
	t.Cleanup(func() { codexDesktopBundleInfo = previous })
}

func TestFindCodexDesktopAppFollowsBothBundleLayouts(t *testing.T) {
	stubCodexDesktopBundleInfo(t, codexDesktopBundleID)
	for layout, codexPath := range map[string]string{
		"codex-cli package": "Contents/Resources/codex-cli/CodexCLI.app/Contents/MacOS/codex",
		"legacy":            "Contents/Resources/codex",
	} {
		t.Run(layout, func(t *testing.T) {
			bundle := fakeCodexDesktopBundle(t, codexPath)
			app, err := findCodexDesktopApp()
			if err != nil {
				t.Fatal(err)
			}
			if app.Codex != filepath.Join(bundle, filepath.FromSlash(codexPath)) || app.Executable != filepath.Join(bundle, "Contents", "MacOS", "ChatGPT") {
				t.Fatalf("app = %+v", app)
			}
		})
	}
	t.Run("no bundled codex", func(t *testing.T) {
		fakeCodexDesktopBundle(t)
		if _, err := findCodexDesktopApp(); err == nil {
			t.Fatal("a bundle without codex was accepted")
		}
	})
	t.Run("other app", func(t *testing.T) {
		stubCodexDesktopBundleInfo(t, "com.openai.chat")
		fakeCodexDesktopBundle(t, "Contents/Resources/codex")
		if _, err := findCodexDesktopApp(); err == nil {
			t.Fatal("a different bundle id was accepted")
		}
	})
}

func TestRunCodexDesktopRejectsArguments(t *testing.T) {
	if code := runCodexDesktop([]string{"extra"}); code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
}

func TestRunCodexDesktopRefusesWhenAppIsOpen(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the open-app check runs after the macOS gate")
	}
	cfg := uxConfig(t)
	stubCodexDesktopBundleInfo(t, codexDesktopBundleID)
	bundle := fakeCodexDesktopBundle(t, "Contents/Resources/codex")
	var checked string
	previous := codexDesktopRunning
	codexDesktopRunning = func(executable string) bool { checked = executable; return true }
	t.Cleanup(func() { codexDesktopRunning = previous })
	if code := runCodexDesktop(nil); code == 0 {
		t.Fatal("an open Codex app should refuse a new session")
	}
	if checked != filepath.Join(bundle, "Contents", "MacOS", "ChatGPT") {
		t.Fatalf("checked %q", checked)
	}
	if entries, _ := os.ReadDir(codexDesktopDir(cfg)); len(entries) != 0 {
		t.Fatal("a refused launch wrote a shim")
	}
}

func TestDeinitCodexDesktopRemovesOnlyItsDirectory(t *testing.T) {
	cfg := uxConfig(t)
	if configured, _ := integrationConfigured(cfg, "codex-desktop"); configured {
		t.Fatal("configured before init")
	}
	if err := os.MkdirAll(filepath.Dir(codexDesktopShimPath(cfg)), 0o700); err != nil {
		t.Fatal(err)
	}
	if configured, _ := integrationConfigured(cfg, "codex-desktop"); !configured {
		t.Fatal("not configured after init")
	}
	if remain, err := otherIntegrationsRemain(cfg, "codex"); err != nil || !remain {
		t.Fatalf("Codex Desktop should keep shared setup: %v, %v", remain, err)
	}
	dir, removed, err := deinitCodexDesktop(cfg)
	if err != nil || !removed || dir != codexDesktopDir(cfg) {
		t.Fatalf("deinit = %s, %v, %v", dir, removed, err)
	}
	if _, err := os.Stat(cfg.DataDir); err != nil {
		t.Fatalf("data dir removed: %v", err)
	}
	if _, removed, err := deinitCodexDesktop(cfg); err != nil || removed {
		t.Fatalf("second deinit = %v, %v", removed, err)
	}
}

func TestCleanupAgentSkillKeepsCopySharedWithCodexDesktop(t *testing.T) {
	cfg := uxConfig(t)
	path, err := agentSkillPath(cfg, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if desktopPath, err := agentSkillPath(cfg, "codex-desktop"); err != nil || desktopPath != path {
		t.Fatalf("Codex Desktop skill = %s, %v; want %s", desktopPath, err, path)
	}
	if err := installAgentSkill(path); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(codexDesktopDir(cfg), 0o700); err != nil {
		t.Fatal(err)
	}
	cleanupAgentSkill(cfg, "codex")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("shared skill removed while Codex Desktop still used Keydris: %v", err)
	}
	if err := os.RemoveAll(codexDesktopDir(cfg)); err != nil {
		t.Fatal(err)
	}
	cleanupAgentSkill(cfg, "codex")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("managed skill retained without an active integration")
	}
}

func TestAgentContextNamesTheCodexDesktopLaunch(t *testing.T) {
	t.Setenv("KEYDRIS_SESSION", "codex-desktop-test")
	t.Setenv(sessionOwnerEnv, sessionOwnerRun)
	t.Setenv(codexDesktopEnv, "1")
	out := captureStdout(t, func() {
		if code := runAgentContext(nil); code != 0 {
			t.Fatalf("code = %d", code)
		}
	})
	if !strings.Contains(out, "keydris codex-desktop") || strings.Contains(out, "must launch with keydris codex.") {
		t.Fatalf("briefing = %s", out)
	}
}

func TestStatusReportsCodexDesktop(t *testing.T) {
	cfg := uxConfig(t)
	stubCodexDesktopBundleInfo(t, codexDesktopBundleID)
	bundle := fakeCodexDesktopBundle(t, "Contents/Resources/codex")
	state := func() (string, string) {
		for _, check := range collectStatus(cfg, "codex-desktop", true, false).Checks {
			if check.Name == "codex-desktop" {
				return check.State, check.Detail
			}
		}
		return "", ""
	}
	if got, _ := state(); got != "error" {
		t.Fatalf("unconfigured state = %q, want error", got)
	}
	if err := os.MkdirAll(codexDesktopDir(cfg), 0o700); err != nil {
		t.Fatal(err)
	}
	if got, detail := state(); got != "ok" || !strings.Contains(detail, bundle) {
		t.Fatalf("configured state = %q (%s)", got, detail)
	}
}
