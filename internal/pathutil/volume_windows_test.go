package pathutil

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHasVolumeNameMatchesStdlib(t *testing.T) {
	t.Parallel()
	corpus := generateComponents(t, `./\?:a1%`, 5)
	corpus = append(corpus, `\\host\share`, `\\.\UNC\h\s`, `\??\C:\x`, `\\?\C:\x`, "ä:foo")
	for _, p := range corpus {
		require.Equal(t, filepath.VolumeName(p) != "", HasVolumeName(p), "path %q", p)
	}
}
