package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"
)

var colorMode = "auto"

type terminalUI struct {
	w               io.Writer
	color, terminal bool
}

func newUI(w io.Writer) terminalUI {
	f, ok := w.(*os.File)
	tty := ok && terminalFile(f)
	enabled := tty && os.Getenv("TERM") != "dumb"
	if _, disabled := os.LookupEnv("NO_COLOR"); disabled {
		enabled = false
	}
	if colorMode == "never" {
		enabled = false
	}
	if colorMode == "always" {
		enabled = true
	}
	return terminalUI{w: w, color: enabled, terminal: tty && os.Getenv("TERM") != "dumb"}
}

// Keep untrusted names, errors and log values from injecting terminal controls.
func terminalText(value string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) || r == '\u202e' || r == '\u202d' {
			return -1
		}
		return r
	}, value)
}
func (u terminalUI) style(code, value string) string {
	value = terminalText(value)
	if !u.color {
		return value
	}
	return "\x1b[" + code + "m" + value + "\x1b[0m"
}
func (u terminalUI) row(state, label, detail string) {
	symbol, code := "[OK]", "32"
	switch state {
	case "warning":
		symbol, code = "[!]", "33"
	case "error":
		symbol, code = "[X]", "31"
	case "inactive":
		symbol, code = "[-]", "90"
	case "working":
		symbol, code = "[..]", "36"
	}
	fmt.Fprintf(u.w, "%s %-15s %s\n", u.style(code, symbol), terminalText(label), terminalText(detail))
}
func (u terminalUI) title(title string)  { fmt.Fprintln(u.w, u.style("1", title)) }
func (u terminalUI) next(command string) { fmt.Fprintf(u.w, "\nNext: %s\n", u.style("1", command)) }

// A spinner represents an ongoing operation, never an invented percentage.
// Redirected output receives one ordinary line per operation.
func (u terminalUI) progress(label string) func(error) {
	if !u.terminal {
		u.row("working", label, "")
		return func(err error) {
			if err != nil {
				u.row("error", label, err.Error())
			} else {
				u.row("ok", label, "Done")
			}
		}
	}
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		frames := []string{"|", "/", "-", "\\"}
		i := 0
		for {
			fmt.Fprintf(u.w, "\r%s %s", u.style("36", frames[i%len(frames)]), terminalText(label))
			i++
			select {
			case <-done:
				return
			case <-ticker.C:
			}
		}
	}()
	var once sync.Once
	return func(err error) {
		once.Do(func() {
			close(done)
			wg.Wait()
			fmt.Fprint(u.w, "\r\x1b[2K")
			if err != nil {
				u.row("error", label, err.Error())
			} else {
				u.row("ok", label, "Done")
			}
		})
	}
}
func parseFlags(fs *flag.FlagSet, args []string) int {
	fs.StringVar(&colorMode, "color", colorMode, "terminal colors: auto, always or never")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(fs.Output(), "%s: unexpected argument %q\n", fs.Name(), fs.Arg(0))
		return 2
	}
	if !validColorMode() {
		fmt.Fprintln(fs.Output(), "--color must be auto, always or never")
		return 2
	}
	return -1
}
func validColorMode() bool {
	return colorMode == "auto" || colorMode == "always" || colorMode == "never"
}
