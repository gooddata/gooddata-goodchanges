// Package version holds the entrypoint for `goodchanges version` — also
// reachable as `-v`/`--version`, which main routes here.
package version

import (
	"fmt"
	"strings"

	"goodchanges/internal/cli"
)

// Version is the embedded VERSION file content, set by main at startup. The
// embed itself must live in main (go:embed cannot reach the repo root from
// this package), so main assigns it before any dispatch.
var Version string

// Schema declares what `goodchanges version` accepts: nothing.
var Schema = cli.Schema{
	MinArgs:     0,
	MaxArgs:     0,
	Description: "print the goodchanges version",
}

// Run prints the version — exactly what main's -v/--version did before the
// multi-command interface.
func Run(args []string, flags map[string][]string) error {
	_ = args
	_ = flags
	fmt.Println(strings.TrimSpace(Version))
	return nil
}
