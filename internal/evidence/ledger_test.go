package evidence

import (
	"os"
	"testing"
)

func TestLedgerChainVerify(t *testing.T) {
	path := t.TempDir() + "/evidence.jsonl"
	l, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	for _, e := range []struct {
		typ     string
		payload map[string]int
	}{
		{"issue", map[string]int{"a": 1}},
		{"allow", map[string]int{"b": 2}},
		{"revoke", map[string]int{"c": 3}},
	} {
		if _, err := l.Append(e.typ, e.payload); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	if err := Verify(path); err != nil {
		t.Fatalf("verify clean chain: %v", err)
	}

	records, err := Read(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("records = %d, want 3", len(records))
	}

	// Tamper with the file: flip a byte in the middle record's payload.
	data, _ := os.ReadFile(path)
	for i := range data {
		if data[i] == '2' {
			data[i] = '9'
			break
		}
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Verify(path); err == nil {
		t.Fatal("expected tampered chain to fail verification")
	}
}
