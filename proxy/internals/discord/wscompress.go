package discord

import (
	"bytes"
	"compress/zlib"
	"io"
	"sync"
)

var zlibSyncFlush = []byte{0x00, 0x00, 0xff, 0xff}

// IsZlibFlush reports whether `data` ends with the Z_SYNC_FLUSH marker that
// terminates a Discord zlib-stream gateway message.
func IsZlibFlush(data []byte) bool {
	return bytes.HasSuffix(data, zlibSyncFlush)
}

// ZlibStreamDecoder maintains a stateful zlib-stream decoder across frames
// of the Discord gateway. It is intended for inspection / logging — the
// original (compressed) payload is what callers forward to bots.
//
// All methods are safe for concurrent use thanks to an internal mutex, but
// the underlying zlib stream is order-sensitive: callers must invoke Decode
// in the same order frames were received from the gateway.
type ZlibStreamDecoder struct {
	mu     sync.Mutex
	pr     *io.PipeReader
	pw     *io.PipeWriter
	reader io.ReadCloser
}

// NewZlibStreamDecoder returns a decoder bound to a fresh deflate stream.
// Callers must invoke Close once the gateway connection terminates.
func NewZlibStreamDecoder() *ZlibStreamDecoder {
	pr, pw := io.Pipe()
	return &ZlibStreamDecoder{pr: pr, pw: pw}
}

// Close releases the underlying pipe and zlib reader.
func (d *ZlibStreamDecoder) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.reader != nil {
		_ = d.reader.Close()
		d.reader = nil
	}
	_ = d.pw.Close()
	_ = d.pr.Close()
}

// Decode appends `data` to the underlying zlib stream and, when `data` ends
// on a Z_SYNC_FLUSH boundary, returns the bytes decompressed so far. For
// non-terminal frames it returns (nil, nil).
func (d *ZlibStreamDecoder) Decode(data []byte) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Pipe.Write blocks until the read side has consumed all bytes. Running
	// it in a goroutine and joining below avoids the original race where
	// successive calls could overlap their writes.
	writeDone := make(chan error, 1)
	go func() {
		_, err := d.pw.Write(data)
		writeDone <- err
	}()

	if d.reader == nil {
		r, err := zlib.NewReader(d.pr)
		if err != nil {
			<-writeDone
			return nil, err
		}
		d.reader = r
	}

	if !IsZlibFlush(data) {
		return nil, <-writeDone
	}

	out, readErr := d.drain(writeDone)
	if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
		return out, readErr
	}
	return out, nil
}

// drain reads from the zlib reader until the writer goroutine has finished
// and one further partial read indicates no more decompressed bytes are
// pending. Caller must hold d.mu.
func (d *ZlibStreamDecoder) drain(writeDone <-chan error) ([]byte, error) {
	var result bytes.Buffer
	buf := make([]byte, 8192)
	for {
		n, err := d.reader.Read(buf)
		if n > 0 {
			result.Write(buf[:n])
		}
		if err != nil {
			return result.Bytes(), err
		}
		if n < len(buf) {
			select {
			case <-writeDone:
				return result.Bytes(), nil
			default:
			}
		}
	}
}
