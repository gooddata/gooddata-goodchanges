package cli

import (
	"fmt"
	"strings"
)

// SplitCommand splits a raw argument vector (such as os.Args) into the command
// and the remaining tokens. The first element is the invocation name and is
// ignored; the second is returned verbatim as the command — even a flag-looking
// token, so the caller can recognise `goodchanges --version` or reject it.
// ok is false when there is no token after the invocation name.
func SplitCommand(argv []string) (command string, rest []string, ok bool) {
	if len(argv) < 2 {
		return "", nil, false
	}
	return argv[1], argv[2:], true
}

// ParseArgs parses a command's tokens (everything after the command word) into
// positional args and flags, guided by the schema:
//
//	--name=value   long flag, value attached with '='
//	--name value   long flag, value is the next token (only if TakesValue)
//	--name         boolean long flag (never consumes the next token)
//	-a value       short alias, value is the next token (only if TakesValue)
//	-a=value       short alias, value attached with '='
//	-avalue        short alias with the value attached
//	--             everything after a bare "--" is positional
//
// Parsing is schema-aware: a flag not declared in the schema is an error
// (unknown flags fail fast), a boolean flag never swallows a following
// positional, and a value flag with no value is an error. Flags are returned
// keyed by their canonical Name — aliases are resolved — with every value kept
// in invocation order. A boolean flag contributes its key with an empty
// (non-nil) slice.
//
// Occurrence-count rules (Repeatable) and value rules (Required,
// AllowedValues) are NOT enforced here — that is Validate's job, so parse
// errors ("this isn't a valid invocation") stay distinct from validation
// errors ("this invocation violates the command's contract").
func ParseArgs(schema Schema, tokens []string) (args []string, flags map[string][]string, err error) {
	args = []string{}
	flags = map[string][]string{}

	onlyArgs := false
	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]

		if onlyArgs || tok == "-" || !strings.HasPrefix(tok, "-") {
			args = append(args, tok)
			continue
		}
		if tok == "--" {
			onlyArgs = true
			continue
		}

		name, attached, hasAttached := splitFlagToken(tok)
		decl := flagByName(schema, name)
		if decl == nil {
			return nil, nil, fmt.Errorf("unknown flag: %s", tok)
		}

		if !decl.TakesValue {
			if hasAttached {
				return nil, nil, fmt.Errorf("flag --%s takes no value", decl.Name)
			}
			if _, seen := flags[decl.Name]; !seen {
				flags[decl.Name] = []string{}
			}
			continue
		}

		value := attached
		if !hasAttached {
			if i+1 >= len(tokens) || strings.HasPrefix(tokens[i+1], "-") {
				return nil, nil, fmt.Errorf("flag --%s requires a value", decl.Name)
			}
			value = tokens[i+1]
			i++
		}
		flags[decl.Name] = append(flags[decl.Name], value)
	}

	return args, flags, nil
}

// splitFlagToken takes a token known to start with '-' (and not to be a bare
// "-"/"--") and splits it into the flag name and any attached value.
//
//	--name=value → ("name", "value", true)
//	--name       → ("name", "", false)
//	-a=value     → ("a", "value", true)
//	-avalue      → ("a", "value", true)
//	-a           → ("a", "", false)
func splitFlagToken(tok string) (name, value string, hasValue bool) {
	if strings.HasPrefix(tok, "--") {
		body := tok[2:]
		if eq := strings.IndexByte(body, '='); eq >= 0 {
			return body[:eq], body[eq+1:], true
		}
		return body, "", false
	}
	body := tok[1:]
	if eq := strings.IndexByte(body, '='); eq >= 0 {
		return body[:eq], body[eq+1:], true
	}
	if len(body) > 1 {
		// -avalue: single-letter alias with the value attached.
		return body[:1], body[1:], true
	}
	return body, "", false
}
