package nccm

import (
	"fmt"
	"strconv"
	"strings"

	shlex "github.com/anmitsu/go-shlex"
)

// CustomOptions contains the typed subset of nccm customargs understood by
// ttydeck. Options are parsed from left to right; when an option is repeated,
// the last value wins.
type CustomOptions struct {
	Mode        string
	Password    string
	PasswordSet bool
	Compression bool
	Port        int
	Identity    string
	Unsupported []string
}

// ParseCustomArgs parses nccm's shell-like customargs string.
//
// nccm first tokenizes customargs with shlex.split and later interpolates the
// resulting words into a shell command. A common nccm configuration therefore
// contains a second layer of quoting, for example:
//
//	-P "'U4*9jCv$#D2D5yt'" -z expect
//
// Values retain that extra quote layer after the first shlex pass. We decode a
// value that is still wholly wrapped in one matching quote pair so callers get
// the effective value nccm passes to its SSH helper.
//
// Unknown options are recorded by option name only. Their potential values are
// consumed but are never copied into Unsupported or returned in an error.
func ParseCustomArgs(raw string) (CustomOptions, error) {
	args, err := shlex.Split(normalizeDoubleQuoteEscapes(raw), true)
	if err != nil {
		return CustomOptions{}, fmt.Errorf("parse nccm customargs: %w", err)
	}

	var options CustomOptions
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch arg {
		case "--":
			return options, nil
		case "-P":
			value, next, err := customOptionValue(args, index, "-P")
			if err != nil {
				return CustomOptions{}, err
			}
			options.Password = value
			options.PasswordSet = true
			index = next
		case "-z":
			value, next, err := customOptionValue(args, index, "-z")
			if err != nil {
				return CustomOptions{}, err
			}
			switch value {
			case "ssh", "ssh-rsa", "expect", "expect-ssh-rsa":
				options.Mode = value
			default:
				return CustomOptions{}, fmt.Errorf("parse nccm customargs: unsupported -z mode")
			}
			index = next
		case "-C":
			options.Compression = true
		case "-p":
			value, next, err := customOptionValue(args, index, "-p")
			if err != nil {
				return CustomOptions{}, err
			}
			port, err := strconv.Atoi(value)
			if err != nil || port < 1 || port > 65535 {
				return CustomOptions{}, fmt.Errorf("parse nccm customargs: -p requires an integer between 1 and 65535")
			}
			options.Port = port
			index = next
		case "-i":
			value, next, err := customOptionValue(args, index, "-i")
			if err != nil {
				return CustomOptions{}, err
			}
			options.Identity = value
			index = next
		default:
			if !strings.HasPrefix(arg, "-") || arg == "-" {
				continue
			}

			name, hasInlineValue := unsupportedOptionName(arg)
			options.Unsupported = append(options.Unsupported, name)
			if !hasInlineValue && index+1 < len(args) && !looksLikeCustomOption(args[index+1]) {
				// Do not retain the next word: it may be a credential or other
				// sensitive value belonging to an unknown option.
				index++
			}
		}
	}
	return options, nil
}

func customOptionValue(args []string, index int, option string) (string, int, error) {
	if index+1 >= len(args) {
		return "", index, fmt.Errorf("parse nccm customargs: option %s requires a value", option)
	}
	value, err := decodeNestedCustomValue(args[index+1])
	if err != nil {
		return "", index, fmt.Errorf("parse nccm customargs: invalid quoting in %s value", option)
	}
	return value, index + 1, nil
}

func decodeNestedCustomValue(value string) (string, error) {
	if value == "" || (value[0] != '\'' && value[0] != '"') {
		return value, nil
	}
	if len(value) == 1 || value[len(value)-1] != value[0] {
		return "", fmt.Errorf("unterminated quotation")
	}

	decoded, err := shlex.Split(normalizeDoubleQuoteEscapes(value), true)
	if err != nil {
		return "", err
	}
	if len(decoded) == 0 {
		return "", nil
	}
	if len(decoded) != 1 {
		return value, nil
	}
	return decoded[0], nil
}

// normalizeDoubleQuoteEscapes applies POSIX double-quote backslash handling
// for dollar signs and backticks before handing the string to go-shlex. The
// library intentionally preserves those backslashes, but a shell removes them.
func normalizeDoubleQuoteEscapes(raw string) string {
	var normalized strings.Builder
	normalized.Grow(len(raw))
	state := byte(0)
	for index := 0; index < len(raw); index++ {
		char := raw[index]
		if char == '\\' && index+1 < len(raw) {
			next := raw[index+1]
			if state == '"' && (next == '$' || next == '`') {
				normalized.WriteByte(next)
				index++
				continue
			}
			normalized.WriteByte(char)
			normalized.WriteByte(next)
			index++
			continue
		}

		if char == '\'' || char == '"' {
			switch state {
			case 0:
				state = char
			case char:
				state = 0
			}
		}
		normalized.WriteByte(char)
	}
	return normalized.String()
}

func unsupportedOptionName(arg string) (string, bool) {
	if strings.HasPrefix(arg, "--") {
		if separator := strings.IndexByte(arg, '='); separator >= 0 {
			return arg[:separator], true
		}
		return arg, false
	}
	if len(arg) > 2 {
		// Short SSH options may carry an attached value. Keep only the option
		// name so a ProxyCommand, password, or token cannot leak through it.
		return arg[:2], true
	}
	return arg, false
}

func looksLikeCustomOption(arg string) bool {
	return strings.HasPrefix(arg, "-") && arg != "-"
}
