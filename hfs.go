package git

import (
	"runtime"
	"unicode"
)

// defaultProtectHFS returns the default value for core.protectHFS
// when not explicitly configured. Matches upstream Git behaviour:
// enabled by default on macOS.
func defaultProtectHFS() bool {
	return runtime.GOOS == "darwin"
}

// hfsIgnoredCodepoints contains Unicode code points that HFS+ ignores
// during path normalization. A path containing these characters between
// the characters of ".git" will be treated as ".git" by HFS+.
//
// See upstream Git utf8.c next_hfs_char() for the full list.
var hfsIgnoredCodepoints = map[rune]struct{}{
	0x200c: {}, // ZERO WIDTH NON-JOINER
	0x200d: {}, // ZERO WIDTH JOINER
	0x200e: {}, // LEFT-TO-RIGHT MARK
	0x200f: {}, // RIGHT-TO-LEFT MARK
	0x202a: {}, // LEFT-TO-RIGHT EMBEDDING
	0x202b: {}, // RIGHT-TO-LEFT EMBEDDING
	0x202c: {}, // POP DIRECTIONAL FORMATTING
	0x202d: {}, // LEFT-TO-RIGHT OVERRIDE
	0x202e: {}, // RIGHT-TO-LEFT OVERRIDE
	0x206a: {}, // INHIBIT SYMMETRIC SWAPPING
	0x206b: {}, // ACTIVATE SYMMETRIC SWAPPING
	0x206c: {}, // INHIBIT ARABIC FORM SHAPING
	0x206d: {}, // ACTIVATE ARABIC FORM SHAPING
	0x206e: {}, // NATIONAL DIGIT SHAPES
	0x206f: {}, // NOMINAL DIGIT SHAPES
	0xfeff: {}, // ZERO WIDTH NO-BREAK SPACE
}

// isHFSDot returns true if part would be treated as ".<needle>"
// on an HFS+ filesystem after stripping HFS-ignored Unicode code
// points and folding ASCII to lower case. needle must be lowercase
// ASCII.
//
// See upstream Git utf8.c is_hfs_dotgit / is_hfs_dotgitmodules.
func isHFSDot(part, needle string) bool {
	runes := []rune(part)
	i := 0

	// skip ignored code points, then expect '.'
	for i < len(runes) {
		if _, ok := hfsIgnoredCodepoints[runes[i]]; !ok {
			break
		}
		i++
	}
	if i >= len(runes) || runes[i] != '.' {
		return false
	}
	i++

	// match needle case-insensitively, skipping ignored code points
	for _, expected := range needle {
		for i < len(runes) {
			if _, ok := hfsIgnoredCodepoints[runes[i]]; !ok {
				break
			}
			i++
		}
		if i >= len(runes) {
			return false
		}
		r := runes[i]
		if r > 127 {
			return false
		}
		if unicode.ToLower(r) != expected {
			return false
		}
		i++
	}

	// skip trailing ignored code points
	for i < len(runes) {
		if _, ok := hfsIgnoredCodepoints[runes[i]]; !ok {
			break
		}
		i++
	}

	// must be at end of component
	return i == len(runes)
}

// isHFSDotGit returns true if part would be treated as ".git" on
// an HFS+ filesystem.
func isHFSDotGit(part string) bool {
	return isHFSDot(part, "git")
}

// isHFSDotGitmodules returns true if part would be treated as
// ".gitmodules" on an HFS+ filesystem.
func isHFSDotGitmodules(part string) bool {
	return isHFSDot(part, "gitmodules")
}
