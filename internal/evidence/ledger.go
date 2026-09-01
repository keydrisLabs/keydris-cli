// Package evidence is an append-only, hash-chained ledger of control-plane
// events (issuance, revocation, allow/deny decisions). Each record's hash
// covers the previous record's hash, so any tampering breaks the chain.
//
// Storage is a JSONL file (one record per line) for zero-ops portability.
package evidence

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Record is a single ledger entry.
type Record struct {
	Seq      int             `json:"seq"`
	Time     string          `json:"time"`
	Type     string          `json:"type"`
	Payload  json.RawMessage `json:"payload"`
	PrevHash string          `json:"prev_hash"`
	Hash     string          `json:"hash"`
}

// Ledger appends hash-chained records to a JSONL file.
type Ledger struct {
	mu       sync.Mutex
	path     string
	lastHash string
	seq      int
}

// Open opens (or creates) the ledger at path, recovering the chain tip.
func Open(path string) (*Ledger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	_ = os.Chmod(filepath.Dir(path), 0o700)
	l := &Ledger{path: path}
	records, err := Read(path)
	if err != nil {
		return nil, err
	}
	if err := verifyRecords(records); err != nil {
		return nil, err
	}
	if n := len(records); n > 0 {
		l.lastHash = records[n-1].Hash
		l.seq = records[n-1].Seq
	}
	return l, nil
}

// Append writes a new record of the given type carrying payload.
func (l *Ledger) Append(typ string, payload any) (Record, error) {
	pb, err := json.Marshal(payload)
	if err != nil {
		return Record{}, err
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	r := Record{
		Seq:      l.seq + 1,
		Time:     time.Now().UTC().Format(time.RFC3339Nano),
		Type:     typ,
		Payload:  pb,
		PrevHash: l.lastHash,
	}
	r.Hash = hashRecord(r)

	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return Record{}, err
	}
	defer f.Close()
	_ = f.Chmod(0o600)

	line, err := json.Marshal(r)
	if err != nil {
		return Record{}, err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		return Record{}, err
	}
	if err := f.Sync(); err != nil {
		return Record{}, err
	}
	l.lastHash = r.Hash
	l.seq = r.Seq
	return r, nil
}

func hashRecord(r Record) string {
	h := sha256.New()
	fmt.Fprintf(h, "%d\n%s\n%s\n%s\n%s", r.Seq, r.Time, r.Type, r.PrevHash, string(r.Payload))
	return hex.EncodeToString(h.Sum(nil))
}

// Read returns all records in the ledger (empty if the file is absent).
func Read(path string) ([]Record, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var out []Record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var r Record
		if err := json.Unmarshal(line, &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, sc.Err()
}

// Verify recomputes the chain and reports the first inconsistency, if any.
func Verify(path string) error {
	records, err := Read(path)
	if err != nil {
		return err
	}
	return verifyRecords(records)
}

func verifyRecords(records []Record) error {
	prev := ""
	for i, r := range records {
		if r.Seq != i+1 {
			return fmt.Errorf("record %d: sequence mismatch (got %d)", i+1, r.Seq)
		}
		if r.PrevHash != prev {
			return fmt.Errorf("record %d: prev_hash mismatch", r.Seq)
		}
		if hashRecord(r) != r.Hash {
			return fmt.Errorf("record %d: hash mismatch (tampered)", r.Seq)
		}
		prev = r.Hash
	}
	return nil
}
