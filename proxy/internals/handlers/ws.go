package handlers

import (
	"bufio"
	"hallmasterorg/hallmaster-proxy/internals/discord"
	"hallmasterorg/hallmaster-proxy/internals/tamper"
	"io"
	"log/slog"
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
	logger *slog.Logger,
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
					logger.Warn("ws read", "dir", "bot->discord", "err", err)
				}
				return
			}

			payload := frame.Payload
			if frame.Header.Masked {
				payload = make([]byte, len(frame.Payload))
				copy(payload, frame.Payload)
				ws.Cipher(payload, frame.Header.Mask, 0)
			}

			logger.Debug("ws frame", "dir", "bot->discord", "opcode", frame.Header.OpCode, "len", len(payload))

			tampered, err := tamperer.WSOutgoing(payload)
			if err != nil {
				logger.Warn("tamper ws outgoing", "err", err)
				tampered = payload
			}

			if err := wsutil.WriteClientMessage(server, frame.Header.OpCode, tampered); err != nil {
				logger.Error("ws write", "dir", "bot->discord", "err", err)
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
					logger.Warn("ws read", "dir", "discord->bot", "err", err)
				}
				return
			}

			logger.Debug("ws frame", "dir", "discord->bot", "opcode", frame.Header.OpCode, "len", len(frame.Payload), "host", host)

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
					logger.Warn("zlib decode", "err", err)
				} else if inspected != nil {
					tamperView = inspected
				}
				if _, err := tamperer.WSIncoming(tamperView); err != nil {
					logger.Warn("tamper ws incoming", "err", err)
				}
			} else {
				tampered, err := tamperer.WSIncoming(frame.Payload)
				if err != nil {
					logger.Warn("tamper ws incoming", "err", err)
				} else {
					outbound = tampered
				}
			}

			if err := wsutil.WriteServerMessage(client, frame.Header.OpCode, outbound); err != nil {
				logger.Error("ws write", "dir", "discord->bot", "err", err)
				return
			}

			if frame.Header.OpCode == ws.OpClose {
				return
			}
		}
	}()

	wg.Wait()
	logger.Info("ws connection closed", "host", host)
}
