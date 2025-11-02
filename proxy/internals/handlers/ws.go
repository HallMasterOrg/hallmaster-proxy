package handlers

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"hallmasterorg/hallmaster-proxy/internals/middlewares"
	"io"
	"log"
	"net"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)
type ZlibDecoder struct {
    pr *io.PipeReader
    pw *io.PipeWriter
    reader io.ReadCloser
}

func NewZlibDecoder() *ZlibDecoder {
    pr, pw := io.Pipe()
    return &ZlibDecoder{
        pr: pr,
        pw: pw,
    }
}
func (d *ZlibDecoder) Decode(data []byte) ([]byte, error) {
    go d.pw.Write(data)
    if d.reader == nil {
        var err error
        d.reader, err = zlib.NewReader(d.pr)
        if err != nil {
            return nil, err
        }
    }
    if !bytes.HasSuffix(data, []byte{0x00, 0x00, 0xff, 0xff}) {
        return nil, nil
    }
    return d.readAvailable()
}

func (d *ZlibDecoder) readAvailable() ([]byte, error) {
    var result bytes.Buffer
    buf := make([]byte, 8192)
    for {
        n, err := d.reader.Read(buf)
        result.Write(buf[:n])
        if n < len(buf) {
            break
        }
        if err != nil {
            if err == io.EOF || err == io.ErrUnexpectedEOF {
                break
            }
            return nil, err
        }
    }
    return result.Bytes(), nil
}

// InspectWS proxies WebSocket traffic between client (bot) and server (Discord)
// with optional zlib decompression and tampering hooks.
// InspectWS proxies WebSocket traffic with correct zlib-stream handling
func InspectWS(
	client net.Conn,
	clientBr *bufio.Reader,
	server net.Conn,
	serverBr *bufio.Reader,
	host string,
	isCompressed bool,
) {
	decoder := NewZlibDecoder()

	go func() {
		src := io.MultiReader(clientBr, client)
		for {
			frame, err := ws.ReadFrame(src)
			if err != nil {
				log.Printf("[WS Bot -> Discord] Read error: %v", err)
				return
			}

			outgoing := make([]byte, len(frame.Payload))
            copy(outgoing, frame.Payload)
            if frame.Header.Masked {
                ws.Cipher(outgoing, frame.Header.Mask, 0)
            }

			log.Printf("[WS Bot -> Discord] Op: %v | Payload: %s",
				frame.Header.OpCode, string(outgoing))

			tampered, err := middlewares.TamperOutgoing(outgoing)
			if err != nil {
				log.Printf("TamperOutgoing error: %v - using original", err)
				tampered = outgoing
			}

			if err := wsutil.WriteClientMessage(server, frame.Header.OpCode, tampered); err != nil {
				log.Printf("[WS Bot -> Discord] Write error: %v", err)
				return
			}

			if frame.Header.OpCode == ws.OpClose {
				return
			}
		}
	}()

	go func() {
		src := io.MultiReader(serverBr, server)
		for {
			frame, err := ws.ReadFrame(src)
			if err != nil {
				log.Printf("[WS Discord -> Bot] Read error: %v", err)
				return
			}

			log.Printf("[WS Discord -> Bot] Op: %v | Compressed Len: %d", frame.Header.OpCode, len(frame.Payload))

			var incoming []byte

			if isCompressed && frame.Header.OpCode == ws.OpBinary {
				incoming, err = decoder.Decode(frame.Payload)
				if err != nil {
					log.Printf("Zlib decode error: %v - forwarding raw compressed data", err)
					incoming = frame.Payload
				} else if incoming == nil {
					continue
				}
			} else {
				incoming = frame.Payload
			}

			log.Printf("[WS Discord -> Bot] %s | Decompressed Len: %d | Payload: %s",
				host, len(incoming), string(incoming))

			tampered, err := middlewares.TamperIncoming(incoming)
			if err != nil {
				log.Printf("TamperIncoming error: %v - using original", err)
				tampered = incoming
			}

			if err := wsutil.WriteServerMessage(client, frame.Header.OpCode, tampered); err != nil {
				log.Printf("[WS Discord -> Bot] Write error: %v", err)
				return
			}

			if frame.Header.OpCode == ws.OpClose {
				return
			}
		}
	}()

	<-make(chan struct{})
	log.Printf("[WS] Connection closed for %s", host)
}
