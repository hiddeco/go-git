package pathutil

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

//nolint:paralleltest // AllocsPerRun must run without parallel tests.
func TestIsHFSDotAllocations(t *testing.T) {
	part := strings.Repeat("a", 40)
	allocations := testing.AllocsPerRun(1000, func() { IsHFSDot(part, ".") })
	require.Zero(t, allocations)
}
