package cli

import (
	"fmt"
	"os"

	"github.com/nocaplabs/keydris-cli/internal/config"
	"github.com/nocaplabs/keydris-cli/internal/evidence"
)

// runLogs implements `keydris logs`: print the hash-chained evidence ledger
// (issuance, allow, deny) and verify the chain integrity.
func runLogs() int {
	cfg := config.Load()

	records, err := evidence.Read(cfg.LedgerPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "keydris logs: read ledger: %v\n", err)
		return 1
	}
	if len(records) == 0 {
		fmt.Printf("evidence ledger %s is empty\n", cfg.LedgerPath)
		return 0
	}

	for _, r := range records {
		fmt.Printf("#%-4d %-7s %s  %s\n", r.Seq, r.Type, r.Time, string(r.Payload))
	}

	if err := evidence.Verify(cfg.LedgerPath); err != nil {
		fmt.Printf("\nchain: TAMPERED (%v)\n", err)
		return 1
	}
	fmt.Printf("\nchain: OK (%d records, tip %s)\n", len(records), shortHash(records[len(records)-1].Hash))
	return 0
}

func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}
