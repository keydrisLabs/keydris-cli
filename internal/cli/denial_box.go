package cli

import (
	"strings"
	"unicode/utf8"
)

// denialBoxReasonCode is the only deny reason_code that represents an actual
// policy rejection from the control plane (see the decision enum in
// internal/runtimecontract/decision.go). Every other deny reason is an
// infrastructure error (session, transport, request shape, ...) and keeps
// the plain one-line message so it doesn't read as if a human-authored
// policy made the call.
const denialBoxReasonCode = "keydris_policy_denied"

const (
	denialBoxMinContentWidth = 44
	denialBoxMaxCommandRunes = 60
)

// formatPolicyDenialBox renders a policy rejection as a bordered box so it
// is unmistakable in the harness transcript, instead of reading like any
// other tool error.
func formatPolicyDenialBox(command, reasonCode string) string {
	rows := []string{
		"⛔  COMMAND DENIED",
		"",
		"Command : " + truncateCommandForBox(command, denialBoxMaxCommandRunes),
		"Policy  : " + reasonCode,
		"Source  : Keydris policy service",
		"Result  : blocked before execution",
	}

	width := denialBoxMinContentWidth
	for _, row := range rows {
		if w := utf8.RuneCountInString(row); w > width {
			width = w
		}
	}

	var b strings.Builder
	writeDenialBoxBorder(&b, width, '╔', '╗')
	writeDenialBoxRow(&b, width, "")
	for _, row := range rows {
		writeDenialBoxRow(&b, width, row)
	}
	writeDenialBoxRow(&b, width, "")
	writeDenialBoxBorder(&b, width, '╚', '╝')
	return strings.TrimSuffix(b.String(), "\n")
}

func writeDenialBoxBorder(b *strings.Builder, width int, left, right rune) {
	b.WriteRune(left)
	b.WriteString(strings.Repeat("═", width+4))
	b.WriteRune(right)
	b.WriteByte('\n')
}

func writeDenialBoxRow(b *strings.Builder, width int, text string) {
	pad := width - utf8.RuneCountInString(text)
	b.WriteString("║  ")
	b.WriteString(text)
	b.WriteString(strings.Repeat(" ", pad))
	b.WriteString("  ║\n")
}

func truncateCommandForBox(command string, maxRunes int) string {
	if utf8.RuneCountInString(command) <= maxRunes {
		return command
	}
	runes := []rune(command)
	return string(runes[:maxRunes-1]) + "…"
}
