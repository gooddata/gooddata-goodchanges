// Package cli holds the schema types, argv parsing, and validation for the
// multi-command interface. Each command package under cmd/ declares its own
// Schema next to its Run entrypoint; main.go registers them in its dispatch
// table, parses the raw argv against the selected schema (unknown flags fail
// fast), validates the result, and only then calls the command's entrypoint
// with the clean (args, flags) pair. The per-command --help output is likewise
// generated from these schemas by main — command packages never see argv or
// help concerns.
package cli

// Flag declares one flag a command accepts.
type Flag struct {
	// Name is the canonical long name, without leading dashes (e.g.
	// "compare-commit" for --compare-commit). Parsed flags are always keyed by
	// this name, regardless of which alias was used on the command line.
	Name string
	// Aliases are alternative spellings, without leading dashes — typically
	// single letters (e.g. "c"), usable as -c. Long aliases are allowed.
	Aliases []string
	// TakesValue: the flag expects a value (--name=value, --name value, -n
	// value, -nvalue). When false the flag is boolean — it never consumes the
	// following token, so `--bool positional` keeps the positional.
	TakesValue bool
	// Required: the flag must appear at least once.
	Required bool
	// Repeatable: the flag may appear more than once (every value is kept, in
	// invocation order). Non-repeatable flags fail validation when repeated.
	Repeatable bool
	// AllowedValues, when non-empty, restricts every value of this flag to one
	// of the listed strings. Only meaningful with TakesValue.
	AllowedValues []string
	// ValueName names the value in help output (e.g. "commit-sha" renders as
	// --compare-commit <commit-sha>). Only meaningful with TakesValue.
	ValueName string
	// Description is the one-line help text for this flag.
	Description string
}

// Schema declares what one command accepts: its positional-argument arity and
// its flags. It is the single source of truth for parsing, validation, and the
// command's generated --help.
type Schema struct {
	// MinArgs/MaxArgs bound the positional-argument count. MaxArgs < 0 means
	// unbounded.
	MinArgs int
	MaxArgs int
	// ArgNames names the positional arguments for help output (e.g.
	// ["target"...]). Purely descriptive; arity is governed by Min/MaxArgs.
	ArgNames []string
	// Flags are the flags this command accepts. Any flag not declared here is
	// rejected during parsing.
	Flags []Flag
	// Description is the one-line summary shown in the top-level command list
	// and at the head of the command's --help.
	Description string
}

// flagByName resolves a parsed flag token (canonical name or alias, no dashes)
// to its declaration. Returns nil when the schema doesn't declare it.
func flagByName(schema Schema, name string) *Flag {
	for i := range schema.Flags {
		f := &schema.Flags[i]
		if f.Name == name {
			return f
		}
		for _, a := range f.Aliases {
			if a == name {
				return f
			}
		}
	}
	return nil
}
