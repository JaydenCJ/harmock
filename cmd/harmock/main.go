// Command harmock serves any HAR capture as a deterministic local mock API
// server. See README.md for the full story.
package main

import (
	"os"

	"github.com/JaydenCJ/harmock/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
