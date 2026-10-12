package cli

import (
	"fmt"
	"strings"
)

// Validate checks a parsed (args, flags) pair against the schema's contract:
// positional arity, required flags, repeatability, and allowed values. It runs
// after ParseArgs (which already rejected unknown flags and malformed tokens);
// a nil error means the pair is safe to hand to the command's entrypoint.
func Validate(schema Schema, args []string, flags map[string][]string) error {
	if len(args) < schema.MinArgs {
		return fmt.Errorf("expected at least %d positional argument(s), got %d", schema.MinArgs, len(args))
	}
	if schema.MaxArgs >= 0 && len(args) > schema.MaxArgs {
		return fmt.Errorf("expected at most %d positional argument(s), got %d", schema.MaxArgs, len(args))
	}

	for i := range schema.Flags {
		f := &schema.Flags[i]
		values, present := flags[f.Name]

		if f.Required && !present {
			return fmt.Errorf("missing required flag --%s", f.Name)
		}
		if !present {
			continue
		}
		if !f.Repeatable && len(values) > 1 {
			return fmt.Errorf("flag --%s given %d times, may appear once", f.Name, len(values))
		}
		if len(f.AllowedValues) > 0 {
			for _, v := range values {
				if !contains(f.AllowedValues, v) {
					return fmt.Errorf("invalid value %q for --%s (allowed: %s)",
						v, f.Name, strings.Join(f.AllowedValues, ", "))
				}
			}
		}
	}

	return nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
