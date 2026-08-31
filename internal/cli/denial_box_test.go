package cli

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestFormatPolicyDenialBoxAlignsBorders(t *testing.T) {
	box := formatPolicyDenialBox("git status", "keydris_policy_denied")
	lines := strings.Split(box, "\n")
	if len(lines) < 3 {
		t.Fatalf("box has too few lines: %q", box)
	}
	width := utf8.RuneCountInString(lines[0])
	for i, line := range lines {
		if got := utf8.RuneCountInString(line); got != width {
			t.Fatalf("line %d width = %d, want %d: %q", i, got, width, line)
		}
	}
	if !strings.HasPrefix(lines[0], "╔") || !strings.HasSuffix(lines[0], "╗") {
		t.Fatalf("top border malformed: %q", lines[0])
	}
	last := lines[len(lines)-1]
	if !strings.HasPrefix(last, "╚") || !strings.HasSuffix(last, "╝") {
		t.Fatalf("bottom border malformed: %q", last)
	}
}

func TestFormatPolicyDenialBoxTruncatesLongCommands(t *testing.T) {
	long := strings.Repeat("a", denialBoxMaxCommandRunes+20)
	box := formatPolicyDenialBox(long, "keydris_policy_denied")
	if strings.Contains(box, long) {
		t.Fatalf("expected long command to be truncated, got: %q", box)
	}
	if !strings.Contains(box, "…") {
		t.Fatalf("expected truncation ellipsis in box: %q", box)
	}
}
