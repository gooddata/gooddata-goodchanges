// Package listprojects holds the entrypoint for `goodchanges list-projects` —
// it prints the rush project list as JSON (formerly the top-level --list flag).
package listprojects

import (
	"encoding/json"
	"fmt"

	"goodchanges/internal/cli"
	"goodchanges/internal/rush"
)

// Schema declares what `goodchanges list-projects` accepts: nothing.
var Schema = cli.Schema{
	MinArgs:     0,
	MaxArgs:     0,
	Description: "print the rush project list as JSON",
}

// Run loads the rush config from the working directory and prints its
// projects as indented JSON on stdout.
func Run(args []string, flags map[string][]string) error {
	_ = args
	_ = flags

	rushConfig, err := rush.LoadConfig(".")
	if err != nil {
		return fmt.Errorf("loading rush config: %w", err)
	}
	data, err := json.MarshalIndent(rushConfig.Projects, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling projects: %w", err)
	}
	fmt.Println(string(data))
	return nil
}
