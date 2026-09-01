package evidence

import (
	"os"
	"strings"
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
	if _, err := Open(path); err == nil {
		t.Fatal("expected daemon ledger open to reject tampered chain")
	}
}

func TestLedgerReadsOneMiBToolParameters(t *testing.T) {
	path := t.TempDir() + "/evidence.jsonl"
	ledger, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	params := strings.Repeat("x", (1<<20)+1024)
	if _, err := ledger.Append("authorize", map[string]any{"tool_params": map[string]string{"payload": params}}); err != nil {
		t.Fatal(err)
	}
	records, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d", len(records))
	}
}
