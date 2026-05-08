package git

import (
	"runtime"
	"strings"
)

// defaultProtectNTFS returns the default value for core.protectNTFS
// when not explicitly configured. Matches upstream Git behaviour:
// enabled by default on Windows.
func defaultProtectNTFS() bool {
	return runtime.GOOS == "windows"
}

// windowsPathReplacer defines the chars that need to be replaced
// as part of windowsValidPath.
var windowsPathReplacer = strings.NewReplacer(" ", "", ".", "")

func windowsValidPath(part string) bool {
	// Bare ".git" is allowed at this layer; rejection of root-level or
	// non-final ".git" components is handled by validPath. This check
	// only catches the Windows-specific variants (`.git ` / `.git.` /
	// `.git::$INDEX_ALLOCATION` etc.) that get normalised back to ".git".
	if len(part) > 4 && strings.EqualFold(part[:4], GitDirName) {
		// For historical reasons, file names that end in spaces or periods are
		// automatically trimmed. Therefore, `.git . . ./` is a valid way to refer
		// to `.git/`.
		if windowsPathReplacer.Replace(part[4:]) == "" {
			return false
		}

		// For yet other historical reasons, NTFS supports so-called "Alternate Data
		// Streams", i.e. metadata associated with a given file, referred to via
		// `<filename>:<stream-name>:<stream-type>`. There exists a default stream
		// type for directories, allowing `.git/` to be accessed via
		// `.git::$INDEX_ALLOCATION/`.
		//
		// For performance reasons, _all_ Alternate Data Streams of `.git/` are
		// forbidden, not just `::$INDEX_ALLOCATION`.
		if part[4:5] == ":" {
			return false
		}
	}
	return !isWindowsReservedName(part)
}

// windowsReservedNames lists the Windows reserved device names.
// A path component is reserved if its base name (ignoring trailing
// spaces, extensions, and NTFS Alternate Data Streams) matches one of
// these case-insensitively.
//
// See upstream Git compat/mingw.c is_valid_win32_path().
var windowsReservedNames = []string{
	"CON", "PRN", "AUX", "NUL",
	"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
	"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9",
	"CONIN$", "CONOUT$",
}

func isWindowsReservedName(part string) bool {
	for _, name := range windowsReservedNames {
		if len(part) < len(name) {
			continue
		}
		if !strings.EqualFold(part[:len(name)], name) {
			continue
		}
		// Exact match or followed by space, dot, colon (ADS), or separator.
		if len(part) == len(name) {
			return true
		}
		switch part[len(name)] {
		case ' ', '.', ':':
			return true
		}
	}
	return false
}

// isNTFSDot returns true if part would be normalised to ".<dotgit>"
// by NTFS. It matches three patterns:
//
//  1. ".<dotgit>" optionally followed by spaces, periods, or an
//     Alternate Data Stream marker (`:`).
//  2. The standard NTFS short name "<dotgit[:6]>~<1..4>" with the
//     same trailing tolerance.
//  3. The fall-back NTFS short name where the first 6 bytes are the
//     deterministic shortname prefix (e.g. "gi7eba" for ".gitmodules"),
//     followed by `~<digit>` and trailing space/period/colon.
//
// shortnamePrefix is the lowercase 6-byte fall-back prefix used by
// NTFS when the standard short name is taken. See upstream Git
// path.c is_ntfs_dot_generic.
func isNTFSDot(part, dotgit, shortnamePrefix string) bool {
	// Pattern 1: ".<dotgit>" + tail.
	if len(part) >= 1+len(dotgit) && part[0] == '.' &&
		strings.EqualFold(part[1:1+len(dotgit)], dotgit) {
		return ntfsOnlySpacesPeriodsADS(part[1+len(dotgit):])
	}

	// Pattern 2: standard NTFS short name.
	if len(dotgit) >= 6 && len(part) >= 8 &&
		strings.EqualFold(part[:6], dotgit[:6]) &&
		part[6] == '~' && part[7] >= '1' && part[7] <= '4' {
		return ntfsOnlySpacesPeriodsADS(part[8:])
	}

	// Pattern 3: fall-back NTFS short name keyed off shortnamePrefix.
	if len(part) < 8 || len(shortnamePrefix) < 6 {
		return false
	}
	sawTilde := false
	for i := 0; i < 8; i++ {
		c := part[i]
		switch {
		case sawTilde:
			if c < '0' || c > '9' {
				return false
			}
		case c == '~':
			i++
			if i >= len(part) || part[i] < '1' || part[i] > '9' {
				return false
			}
			sawTilde = true
		case i >= 6:
			return false
		case c >= 0x80:
			return false
		default:
			if asciiToLower(c) != shortnamePrefix[i] {
				return false
			}
		}
	}
	return ntfsOnlySpacesPeriodsADS(part[8:])
}

// ntfsOnlySpacesPeriodsADS returns true if rest is empty or composed
// entirely of space/period bytes terminating in a colon (Alternate
// Data Stream marker) or end-of-string.
func ntfsOnlySpacesPeriodsADS(rest string) bool {
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		if c == ':' {
			return true
		}
		if c != ' ' && c != '.' {
			return false
		}
	}
	return true
}

func asciiToLower(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 32
	}
	return c
}

// isNTFSDotGitmodules returns true if part would be treated as
// ".gitmodules" on an NTFS filesystem after stripping trailing
// spaces, periods, and Alternate Data Streams.
//
// See upstream Git path.c is_ntfs_dotgitmodules.
func isNTFSDotGitmodules(part string) bool {
	return isNTFSDot(part, "gitmodules", "gi7eba")
}
