package main

import (
	_ "embed"
	"fmt"
	"io"
	"os"
	"sort"

	"goodchanges/cmd/detect"
	listprojects "goodchanges/cmd/list_projects"
	versioncmd "goodchanges/cmd/version"
	"goodchanges/internal/cli"
)

//go:embed VERSION
var version string

// command is one entry of the multi-command dispatch table: the entrypoint and
// the arg/flag schema main parses and validates against before calling it.
type command struct {
	run    func(args []string, flags map[string][]string) error
	schema cli.Schema
}

// commands maps each CLI verb to its entrypoint and schema. Verbs are parsed
// and validated here in main; entrypoints receive the clean (args, flags) pair
// and never touch argv themselves.
var commands = map[string]command{
	"detect":        {run: detect.Run, schema: detect.Schema},
	"list-projects": {run: listprojects.Run, schema: listprojects.Schema},
	"version":       {run: versioncmd.Run, schema: versioncmd.Schema},
}

func usage(w io.Writer) {
	fmt.Fprintf(w, "usage: goodchanges <command> [flags]\n\nCommands:\n")
	names := make([]string, 0, len(commands))
	for name := range commands {
		names = append(names, name)
	}
	sort.Strings(names)
	width := 0
	for _, n := range names {
		if len(n) > width {
			width = len(n)
		}
	}
	for _, n := range names {
		fmt.Fprintf(w, "  %-*s  %s\n", width, n, commands[n].schema.Description)
	}
	fmt.Fprintf(w, "\nRun 'goodchanges <command> --help' for a command's flags.\n")
}

// wantsHelp reports whether the tokens ask for help: a literal --help or -h
// anywhere before a bare "--" (everything after "--" is positional and never
// treated as a help request).
func wantsHelp(tokens []string) bool {
	for _, tok := range tokens {
		if tok == "--" {
			return false
		}
		if tok == "--help" || tok == "-h" {
			return true
		}
	}
	return false
}

func main() {
	// The VERSION embed lives here (go:embed can't reach the repo root from
	// cmd/version), so hand it over before any dispatch can print it.
	versioncmd.Version = version

	verb, rest, ok := cli.SplitCommand(os.Args)
	if !ok {
		usage(os.Stderr)
		os.Exit(1)
	}

	// Flag spellings handled at the top level: -v/--version print the version
	// like the `version` command; --help/-h print the command list.
	switch verb {
	case "-v", "--version":
		if err := versioncmd.Run(nil, nil); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	case "--help", "-h":
		usage(os.Stdout)
		return
	}

	cmd, registered := commands[verb]
	if !registered {
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", verb)
		usage(os.Stderr)
		os.Exit(1)
	}

	// --help is answered here from the command's schema — it never reaches the
	// command's entrypoint (and would otherwise be rejected as an unknown flag).
	if wantsHelp(rest) {
		fmt.Print(cli.Help("goodchanges "+verb, cmd.schema))
		return
	}

	args, flags, err := cli.ParseArgs(cmd.schema, rest)
	if err == nil {
		err = cli.Validate(cmd.schema, args, flags)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", verb, err)
		os.Exit(1)
	}

	if err := cmd.run(args, flags); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
