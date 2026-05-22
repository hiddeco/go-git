package sideband

// Type sideband type "side-band" or "side-band-64k"
type Type int8

const (
	// Sideband legacy sideband type up to 995-byte data chunks
	// (1000-byte pkt-line frames).
	Sideband Type = iota
	// Sideband64k sideband type up to 65515-byte data chunks
	// (65520-byte pkt-line frames).
	Sideband64k Type = iota

	// MaxPackedSize for Sideband type
	MaxPackedSize = 1000
	// MaxPackedSize64k for Sideband64k type
	MaxPackedSize64k = 65520
)

// Channel sideband channel
type Channel byte

// WithPayload encode the payload as a message
func (ch Channel) WithPayload(payload []byte) []byte {
	return append([]byte{byte(ch)}, payload...)
}

const (
	// PackData packfile content
	PackData Channel = 1
	// ProgressMessage progress messages
	ProgressMessage Channel = 2
	// ErrorMessage fatal error message just before stream aborts
	ErrorMessage Channel = 3
)
