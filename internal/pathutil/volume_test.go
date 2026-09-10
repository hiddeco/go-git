package pathutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHasVolumeName(t *testing.T) {
	t.Parallel()
	for p, want := range map[string]bool{
		`C:foo`: true, `1:foo`: true, `%:x`: true, `x:`: true, `.:x`: true,
		`\\host\share`: true, `//host/share`: true, `\\srv`: true,
		`\\`: true, `//`: true, `\/`: true, `\\.`: true, `\\?`: true,
		`\??`: true, `\??\C:\x`: true, `\\?\C:\x`: true,
		`\\.\UNC\h\s`: true, `::`: true,
		`a/b:c`: false, `/foo`: false, `foo`: false, `\?`: false, `\`: false, `:`: false,
	} {
		t.Run(p, func(t *testing.T) { t.Parallel(); assert.Equal(t, want, HasVolumeName(p)) })
	}
}
