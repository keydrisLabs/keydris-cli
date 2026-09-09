package cli

import (
	"context"
	"flag"
	"fmt"
	"github.com/keydrisLabs/keydris-cli/internal/config"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func runProxyLogs(args []string) int {
	fs := flag.NewFlagSet("proxy logs", flag.ContinueOnError)
	follow := fs.Bool("follow", false, "follow new daemon output until Ctrl+C")
	lines := fs.Int("lines", 50, "number of recent lines (1-10000; up to 1 MiB)")
	if code := parseFlags(fs, args); code >= 0 {
		return code
	}
	if *lines < 1 || *lines > 10000 {
		fmt.Fprintln(os.Stderr, "proxy logs: --lines must be 1-10000")
		return 2
	}
	cfg := config.Load()
	if err := cfg.ValidatePaths(); err != nil {
		newUI(os.Stderr).row("error", "Paths", err.Error())
		return 1
	}
	path := filepath.Join(cfg.DataDir, "proxy.log")
	f, err := os.Open(path)
	if err != nil {
		newUI(os.Stderr).row("warning", "Proxy log", "No readable log at "+path)
		return 1
	}
	defer func() { f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return 1
	}
	offset := int64(0)
	if st.Size() > 1<<20 {
		offset = st.Size() - (1 << 20)
	}
	if _, err = f.Seek(offset, io.SeekStart); err != nil {
		return 1
	}
	data, err := io.ReadAll(io.LimitReader(f, 1<<20))
	if err != nil {
		return 1
	}
	offset += int64(len(data))
	text := string(data)
	if st.Size() > 1<<20 {
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			text = text[i+1:]
		}
	}
	recent := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if len(recent) > *lines {
		recent = recent[len(recent)-*lines:]
	}
	if len(data) > 0 {
		for _, line := range recent {
			fmt.Fprintln(os.Stdout, terminalText(line))
		}
	}
	if !*follow {
		return 0
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return 0
		case <-ticker.C:
		}
		current, err := os.Stat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			newUI(os.Stderr).row("error", "Proxy log", err.Error())
			return 1
		}
		if !os.SameFile(st, current) || current.Size() < offset {
			replacement, err := os.Open(path)
			if err != nil {
				continue
			}
			f.Close()
			f = replacement
			offset = 0
			st = current
		}
		data, err := io.ReadAll(io.LimitReader(f, 64<<10))
		if err != nil {
			return 1
		}
		offset += int64(len(data))
		// Preserve line boundaries, strip terminal escapes supplied by log values.
		for _, part := range strings.SplitAfter(string(data), "\n") {
			if part == "" {
				continue
			}
			fmt.Fprint(os.Stdout, terminalText(strings.TrimSuffix(part, "\n")))
			if strings.HasSuffix(part, "\n") {
				fmt.Fprintln(os.Stdout)
			}
		}
	}
}
