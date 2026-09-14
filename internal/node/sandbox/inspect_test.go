package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

// HasKeydrisHooks distinguishes an unused tool from a damaged Keydris setup, so
// deinit only clears shared state when the other integration no longer uses us.
func TestHasKeydrisHooks(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		write   bool
		want    bool
		wantErr bool
	}{
		{name: "missing file", want: false},
		{name: "empty file", write: true, want: false},
		{
			name:    "keydris session start",
			content: `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"keydris __session-start"}]}]}}`,
			write:   true,
			want:    true,
		},
		{
			name:    "keydris permission request",
			content: `{"hooks":{"PermissionRequest":[{"hooks":[{"type":"command","command":"/usr/local/bin/keydris __pretool-use"}]}]}}`,
			write:   true,
			want:    true,
		},
		{
			name:    "windows shim path",
			content: `{"hooks":{"SessionEnd":[{"hooks":[{"type":"command","command":"C:\\Tools\\keydris.exe __session-end"}]}]}}`,
			write:   true,
			want:    true,
		},
		{
			name:    "user hook only",
			content: `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"echo hello"}]}]}}`,
			write:   true,
			want:    false,
		},
		{name: "unrelated settings", content: `{"env":{"FOO":"bar"}}`, write: true, want: false},
		{name: "malformed json", content: `{"hooks":`, write: true, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			if tc.write {
				if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := HasKeydrisHooks(path)
			if (err != nil) != tc.wantErr {
				t.Fatalf("HasKeydrisHooks() error = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("HasKeydrisHooks() = %v, want %v", got, tc.want)
			}
		})
	}
}
