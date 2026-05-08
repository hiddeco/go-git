package config

import (
	"strconv"
	"strings"
)

// OptBool is a tri-state boolean: unset, explicitly false, or explicitly true.
// Its zero value (OptBoolUnset) means the setting was not specified, which
// allows merge logic based on reflect.Value.IsZero to skip unset fields while
// still letting an explicit "false" override a previously set "true".
type OptBool byte

const (
	// OptBoolUnset indicates the setting was not specified.
	OptBoolUnset OptBool = iota
	// OptBoolFalse indicates the setting was explicitly set to false.
	OptBoolFalse
	// OptBoolTrue indicates the setting was explicitly set to true.
	OptBoolTrue
)

// NewOptBool converts a plain bool into an OptBool.
func NewOptBool(v bool) OptBool {
	if v {
		return OptBoolTrue
	}
	return OptBoolFalse
}

// IsTrue returns whether the value is explicitly true.
func (o OptBool) IsTrue() bool { return o == OptBoolTrue }

// IsSet returns whether the value was explicitly specified (true or false).
func (o OptBool) IsSet() bool { return o != OptBoolUnset }

func (o OptBool) String() string {
	switch o {
	case OptBoolTrue:
		return "true"
	case OptBoolFalse:
		return "false"
	default:
		return "unset"
	}
}

// FormatBool returns the strconv-formatted value. Only meaningful when IsSet.
func (o OptBool) FormatBool() string {
	return strconv.FormatBool(o.IsTrue())
}

// parseConfigBool parses a Git-style boolean string. Accepts
// "true"/"yes"/"on"/"1" as true and "false"/"no"/"off"/"0" as false,
// case-insensitively. An empty or unrecognised value returns
// OptBoolUnset, leaving the platform default in effect.
//
// Mirrors upstream Git's git_parse_maybe_bool[1] for the values that
// matter for security toggles like core.protectNTFS / core.protectHFS.
//
// [1]: https://github.com/git/git/blob/564d0252ca632e0264ed670534a51d18a689ef5d/config.c#L1242
func parseConfigBool(v string) OptBool {
	switch strings.ToLower(v) {
	case "true", "yes", "on", "1":
		return OptBoolTrue
	case "false", "no", "off", "0":
		return OptBoolFalse
	}
	return OptBoolUnset
}
