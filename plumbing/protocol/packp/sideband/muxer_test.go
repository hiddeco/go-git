package sideband

import (
	"bytes"
	"strconv"

	"github.com/go-git/go-git/v6/plumbing/format/pktline"
)

func (s *SidebandSuite) TestMuxerWrite() {
	buf := bytes.NewBuffer(nil)

	m := NewMuxer(Sideband, buf)

	// 1998 user bytes are chunked as 995 + 995 + 8 (each frame caps at
	// MaxPackedSize), yielding three pkt-lines of 1000 + 1000 + 13.
	n, err := m.Write(bytes.Repeat([]byte{'F'}, (MaxPackedSize-1)*2))
	s.NoError(err)
	s.Equal(1998, n)
	s.Equal(2013, buf.Len())
}

// Regression: muxer.max was MaxPackedSize64k - 1 (65519), which exceeded
// pktline.MaxPayloadSize (65516) and produced ErrPayloadTooLong on
// near-maximum writes. The correct chunk is MaxPackedSize64k - 5 (65515).
func (s *SidebandSuite) TestMuxerWriteSideband64kAtFrameLimit() {
	const chunk = MaxPackedSize64k - pktline.LenSize - 1

	buf := bytes.NewBuffer(nil)
	m := NewMuxer(Sideband64k, buf)

	n, err := m.Write(bytes.Repeat([]byte{'F'}, chunk+1))
	s.NoError(err)
	s.Equal(chunk+1, n)
	s.Equal(MaxPackedSize64k+pktline.LenSize+1+1, buf.Len())

	got := buf.Bytes()
	first, err := strconv.ParseUint(string(got[:pktline.LenSize]), 16, 32)
	s.NoError(err)
	s.Equal(uint64(MaxPackedSize64k), first)
	s.Equal(byte(PackData), got[pktline.LenSize])

	second, err := strconv.ParseUint(string(got[MaxPackedSize64k:MaxPackedSize64k+pktline.LenSize]), 16, 32)
	s.NoError(err)
	s.Equal(uint64(pktline.LenSize+1+1), second)
	s.Equal(byte(PackData), got[MaxPackedSize64k+pktline.LenSize])
}

func (s *SidebandSuite) TestMuxerWriteChannelMultipleChannels() {
	buf := bytes.NewBuffer(nil)

	m := NewMuxer(Sideband, buf)

	n, err := m.WriteChannel(PackData, bytes.Repeat([]byte{'D'}, 4))
	s.NoError(err)
	s.Equal(4, n)

	n, err = m.WriteChannel(ProgressMessage, bytes.Repeat([]byte{'P'}, 4))
	s.NoError(err)
	s.Equal(4, n)

	n, err = m.WriteChannel(PackData, bytes.Repeat([]byte{'D'}, 4))
	s.NoError(err)
	s.Equal(4, n)

	s.Equal(27, buf.Len())
	s.Equal("0009\x01DDDD0009\x02PPPP0009\x01DDDD", buf.String())
}
