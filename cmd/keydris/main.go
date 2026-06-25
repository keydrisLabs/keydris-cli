// Command keydris is the single user-facing binary (CLI + node daemon) for the
// Keydris POC.
package main

import (
	"os"

	"github.com/keydrisLabs/keydris-cli/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
