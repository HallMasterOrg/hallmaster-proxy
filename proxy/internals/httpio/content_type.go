package httpio

import "strings"

var binaryContentTypes = []string{
	"image/",
	"video/",
	"audio/",
	"application/octet-stream",
	"application/zip",
	"font/",
}

// isBinary reports whether a Content-Type designates an opaque binary payload
// that is not worth decoding for logging or tampering.
func isBinary(contentType string) bool {
	ct := strings.ToLower(contentType)
	for _, t := range binaryContentTypes {
		if strings.Contains(ct, t) {
			return true
		}
	}
	return false
}
