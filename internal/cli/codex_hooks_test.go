package cli

import (
	"bufio"
	"encoding/json"
	"io"
	"reflect"
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

func TestGovernedCodexOverridesRequireEveryHookTrusted(t *testing.T) {
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

			overrides, err := governedCodexOverrides("/app/codex", codexHooks("'/bin/keydris'"))
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

// codexConfigPlacements are command lines and where the governed -c values
// must land in them, given those values as flags. The Codex Desktop shim and
// withCodexConfig apply the same rule.
func codexConfigPlacements(ours []string) map[string]struct{ args, want []string } {
	with := func(before []string, after ...string) []string {
		return append(append(append([]string{}, before...), ours...), after...)
	}
	return map[string]struct{ args, want []string }{
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
		"exec with -c before the prompt": {
			[]string{"exec", "--sandbox", "workspace-write", "-c", `approval_policy="never"`, "--json", "list files"},
			with([]string{"exec", "--sandbox", "workspace-write", "-c", `approval_policy="never"`}, "--json", "list files"),
		},
		"attached values": {
			[]string{"--config=a=1", "app-server", "-cb=2", "--analytics-default-enabled"},
			with([]string{"--config=a=1", "app-server", "-cb=2"}, "--analytics-default-enabled"),
		},
		"-c without a value": {
			[]string{"app-server", "-c"},
			with(nil, "app-server", "-c"),
		},
		"command after --": {
			[]string{"sandbox", "macos", "--", "sh", "-c", "echo 'hi there'"},
			with(nil, "sandbox", "macos", "--", "sh", "-c", "echo 'hi there'"),
		},
		"-c before a command after --": {
			[]string{"-c", "a=1", "sandbox", "macos", "--", "sh", "-c", "echo 'hi there'"},
			with([]string{"-c", "a=1"}, "sandbox", "macos", "--", "sh", "-c", "echo 'hi there'"),
		},
	}
}

func TestWithCodexConfigJoinsTheCallersLastConfigGroup(t *testing.T) {
	values := []string{"features.hooks=true", `hooks.state={"k"={enabled=true}}`}
	for name, tc := range codexConfigPlacements([]string{"-c", values[0], "-c", values[1]}) {
		t.Run(name, func(t *testing.T) {
			if got := withCodexConfig(tc.args, values); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("arguments = %q\nwant        %q", got, tc.want)
			}
		})
	}
}
