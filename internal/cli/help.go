package cli

import (
	"fmt"
	"strings"
)

// Help renders a command's --help text from its schema — the schema is the
// single source of truth, so help never drifts from what parsing/validation
// actually accept. invocation is the full command spelling to show in the
// usage line (e.g. "goodchanges detect"). main prints this and exits;
// command entrypoints never see --help.
func Help(invocation string, schema Schema) string {
	var b strings.Builder

	fmt.Fprintf(&b, "usage: %s%s", invocation, usageArgs(schema))
	if len(schema.Flags) > 0 {
		b.WriteString(" [flags]")
	}
	b.WriteString("\n")

	if schema.Description != "" {
		fmt.Fprintf(&b, "\n  %s\n", schema.Description)
	}

	if len(schema.Flags) > 0 {
		b.WriteString("\nFlags:\n")
		left := make([]string, len(schema.Flags))
		for i := range schema.Flags {
			left[i] = flagInvocation(&schema.Flags[i])
		}
		width := 0
		for _, l := range left {
			if len(l) > width {
				width = len(l)
			}
		}
		for i := range schema.Flags {
			fmt.Fprintf(&b, "  %-*s  %s\n", width, left[i], flagDescription(&schema.Flags[i]))
		}
	}

	return b.String()
}

// usageArgs renders the positional-argument part of the usage line from the
// schema's arity and ArgNames: required args as <name>, optional as [<name>],
// with a trailing "..." when MaxArgs is unbounded.
func usageArgs(schema Schema) string {
	var b strings.Builder
	count := schema.MaxArgs
	if count < 0 {
		count = schema.MinArgs
	}
	if count < schema.MinArgs {
		count = schema.MinArgs
	}
	name := func(i int) string {
		if i < len(schema.ArgNames) {
			return schema.ArgNames[i]
		}
		return "arg"
	}
	for i := 0; i < count; i++ {
		if i < schema.MinArgs {
			fmt.Fprintf(&b, " <%s>", name(i))
		} else {
			fmt.Fprintf(&b, " [<%s>]", name(i))
		}
	}
	if schema.MaxArgs < 0 {
		b.WriteString(" ...")
	}
	return b.String()
}

// flagInvocation renders the left column of one flag line:
// aliases first (shortest spelling first), then the canonical name, then the
// value placeholder — e.g. "-c, --compare-commit <commit>".
func flagInvocation(f *Flag) string {
	var parts []string
	for _, a := range f.Aliases {
		parts = append(parts, dashed(a))
	}
	parts = append(parts, "--"+f.Name)
	s := strings.Join(parts, ", ")
	if f.TakesValue {
		v := f.ValueName
		if v == "" {
			v = "value"
		}
		s += " <" + v + ">"
	}
	return s
}

// flagDescription renders the right column: the description plus any
// schema-derived qualifiers (required, repeatable, allowed values).
func flagDescription(f *Flag) string {
	s := f.Description
	var quals []string
	if f.Required {
		quals = append(quals, "required")
	}
	if f.Repeatable {
		quals = append(quals, "repeatable")
	}
	if len(f.AllowedValues) > 0 {
		quals = append(quals, "one of: "+strings.Join(f.AllowedValues, ", "))
	}
	if len(quals) > 0 {
		if s != "" {
			s += " "
		}
		s += "(" + strings.Join(quals, "; ") + ")"
	}
	return s
}

// dashed prefixes an alias with the right number of dashes: single-letter
// aliases render as -a, longer ones as --name.
func dashed(alias string) string {
	if len(alias) == 1 {
		return "-" + alias
	}
	return "--" + alias
}
