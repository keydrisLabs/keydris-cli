package sessionstate

import (
	"os"
	"runtime"
	"testing"
)

func TestSaveAtomicallyReplacesSessionState(t *testing.T) {
	dir := t.TempDir()
	first := State{SessionID: "session-1", ULID: "old", KIT: "old-kit"}
	if err := Save(dir, first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.ULID = "new"
	second.KIT = "new-kit"
	if err := Save(dir, second); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir, first.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ULID != "new" || got.KIT != "new-kit" {
		t.Fatalf("got stale state: %+v", got)
	}
	info, err := os.Stat(filepathForTest(t, dir, first.SessionID))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("state permissions are too broad: %v", info.Mode().Perm())
	}
}

func filepathForTest(t *testing.T, dir, sessionID string) string {
	t.Helper()
	path, err := Path(dir, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	return path
}
