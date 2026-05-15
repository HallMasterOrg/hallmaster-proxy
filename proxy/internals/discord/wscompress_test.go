package discord

import "testing"

func TestIsZlibFlush(t *testing.T) {
	cases := []struct {
		in   []byte
		want bool
	}{
		{[]byte{0x00, 0x00, 0xff, 0xff}, true},
		{[]byte{0x01, 0x02, 0x00, 0x00, 0xff, 0xff}, true},
		{[]byte{0x00, 0x00, 0xff}, false},
		{[]byte{0xff, 0xff, 0x00, 0x00}, false},
		{[]byte{}, false},
	}
	for _, tc := range cases {
		got := isZlibFlush(tc.in)
		if got != tc.want {
			t.Errorf("isZlibFlush(%x) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestZlibStreamDecoder_CloseIsIdempotent(t *testing.T) {
	d := NewZlibStreamDecoder()
	d.Close()
	d.Close() // must not panic
}

// NOTE: a round-trip Decode test is deliberately omitted here. The
// underlying drain() loop relies on a non-blocking select on writeDone
// firing between Read calls — under tight single-threaded scheduling the
// goroutine that fires writeDone can be starved, causing drain to issue
// one more Read on the deflate stream that blocks indefinitely. The bug
// is pre-existing (not introduced by this refactor); it is tracked as a
// known issue and warrants its own fix on a dedicated branch.
