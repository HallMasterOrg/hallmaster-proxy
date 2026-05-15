package httpio

import "testing"

func TestIsBinary(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"image/png", true},
		{"image/jpeg; charset=binary", true},
		{"video/mp4", true},
		{"audio/ogg", true},
		{"application/octet-stream", true},
		{"application/zip", true},
		{"font/woff2", true},
		{"IMAGE/PNG", true},

		{"application/json", false},
		{"text/html; charset=utf-8", false},
		{"text/plain", false},
		{"application/xml", false},
		{"", false},
	}
	for _, tc := range cases {
		got := isBinary(tc.in)
		if got != tc.want {
			t.Errorf("isBinary(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
