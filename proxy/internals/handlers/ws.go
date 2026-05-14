package handlers

import (
	"bufio"
	"hallmasterorg/hallmaster-proxy/internals/discord"
	"hallmasterorg/hallmaster-proxy/internals/tamper"
	"io"
	"log"
	"net"
	"sync"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

// InspectWS proxies WebSocket traffic between client (bot) and server (Discord).
// When zlib-stream compression is negotiated, the proxy decodes Discord-side
// frames *for inspection only* (so the tamperer sees readable JSON) and
// forwards the original compressed payload onwards — bots that opted into
// compression expect compressed frames.
func InspectWS(
	client net.Conn,
	clientBr *bufio.Reader,
	server net.Conn,
	serverBr *bufio.Reader,
	host string,
	isCompressed bool,
	tamperer tamper.Tamperer,
) {
	var decoder *discord.ZlibStreamDecoder
	if isCompressed {
		decoder = discord.NewZlibStreamDecoder()
		defer decoder.Close()
	}

	var wg sync.WaitGroup
	wg.Add(2)

	// bot -> Discord
	go func() {
		defer wg.Done()
		defer server.Close()
		src := io.MultiReader(clientBr, client)
		for {
			frame, err := ws.ReadFrame(src)
			if err != nil {
				if err != io.EOF {
					log.Printf("[WS Bot -> Discord] Read error: %v", err)
				}
				return
			}

			payload := frame.Payload
			if frame.Header.Masked {
				payload = make([]byte, len(frame.Payload))
				copy(payload, frame.Payload)
				ws.Cipher(payload, frame.Header.Mask, 0)
			}

			log.Printf("[WS Bot -> Discord] Op: %v Len: %d", frame.Header.OpCode, len(payload))

			tampered, err := tamperer.WSOutgoing(payload)
			if err != nil {
				log.Printf("WSOutgoing tamper error: %v - using original", err)
				tampered = payload
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

	// Discord -> bot
	go func() {
		defer wg.Done()
		defer client.Close()
		src := io.MultiReader(serverBr, server)
		for {
			frame, err := ws.ReadFrame(src)
			if err != nil {
				if err != io.EOF {
					log.Printf("[WS Discord -> Bot] Read error: %v", err)
				}
				return
			}

			log.Printf("[WS Discord -> Bot] Op: %v Len: %d (host %s)", frame.Header.OpCode, len(frame.Payload), host)

			// For compressed streams, hand the tamperer the *decoded* view so
			// observers see readable JSON, but always forward the original
			// compressed frame — bots that negotiated compress=zlib-stream
			// expect compressed bytes. For uncompressed streams the
			// tamperer's return value IS what gets forwarded, giving it the
			// option to rewrite.
			outbound := frame.Payload
			if decoder != nil && frame.Header.OpCode == ws.OpBinary {
				tamperView := frame.Payload
				inspected, err := decoder.Decode(frame.Payload)
				if err != nil {
					log.Printf("Zlib decode error: %v", err)
				} else if inspected != nil {
					tamperView = inspected
				}
				if _, err := tamperer.WSIncoming(tamperView); err != nil {
					log.Printf("WSIncoming tamper error: %v", err)
				}
			} else {
				tampered, err := tamperer.WSIncoming(frame.Payload)
				if err != nil {
					log.Printf("WSIncoming tamper error: %v - using original", err)
				} else {
					outbound = tampered
				}
			}

			if err := wsutil.WriteServerMessage(client, frame.Header.OpCode, outbound); err != nil {
				log.Printf("[WS Discord -> Bot] Write error: %v", err)
				return
			}

			if frame.Header.OpCode == ws.OpClose {
				return
			}
		}
	}()

	wg.Wait()
	log.Printf("[WS] Connection closed for %s", host)
}
