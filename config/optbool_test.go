package config

import "testing"

func TestParseConfigBool(t *testing.T) {
	tests := []struct {
		in   string
		want OptBool
	}{
		{"true", OptBoolTrue},
		{"True", OptBoolTrue},
		{"TRUE", OptBoolTrue},
		{"yes", OptBoolTrue},
		{"Yes", OptBoolTrue},
		{"on", OptBoolTrue},
		{"1", OptBoolTrue},
		{"false", OptBoolFalse},
		{"FALSE", OptBoolFalse},
		{"no", OptBoolFalse},
		{"off", OptBoolFalse},
		{"0", OptBoolFalse},
		{"", OptBoolUnset},
		{"maybe", OptBoolUnset},
		{"2", OptBoolUnset},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			if got := parseConfigBool(tc.in); got != tc.want {
				t.Errorf("parseConfigBool(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
