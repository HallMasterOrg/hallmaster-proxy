package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

// Intents the shard identifies with: GUILDS | GUILD_MESSAGES |
// DIRECT_MESSAGES | MESSAGE_CONTENT. Injected events carry no guild_id,
// so the mock treats them as DMs and filters them without 1<<12.
const intents = 1<<0 | 1<<9 | 1<<12 | 1<<15

// noncePrefix tags MESSAGE_CREATE events the driver injected; the suffix
// is the op index, so the reader can match an arrival to its send time.
const noncePrefix = "stress-"

type gatewayFrame struct {
	Op int             `json:"op"`
	T  string          `json:"t"`
	D  json.RawMessage `json:"d"`
}

// connectGateway plays a minimal discord.js shard: HELLO → IDENTIFY →
// READY, then heartbeats in the background and calls onEvent(op index)
// for every injected MESSAGE_CREATE until ctx is cancelled or the socket
// dies. It returns once READY has arrived.
func connectGateway(ctx context.Context, url, token string, tlsCfg *tls.Config, onEvent func(int)) error {
	conn, br, _, err := ws.Dialer{TLSConfig: tlsCfg, Timeout: 10 * time.Second}.Dial(ctx, url)
	if err != nil {
		return fmt.Errorf("dial %s: %w", url, err)
	}
	// The server may have sent HELLO in the same packet as the upgrade
	// response; gobwas hands those bytes back in br.
	var r io.Reader = conn
	if br != nil {
		r = io.MultiReader(br, conn)
	}
	rw := struct {
		io.Reader
		io.Writer
	}{r, conn}

	var hello struct {
		D struct {
			HeartbeatInterval int `json:"heartbeat_interval"`
		} `json:"d"`
	}
	if err := readJSON(rw, &hello); err != nil {
		_ = conn.Close()
		return fmt.Errorf("read HELLO: %w", err)
	}

	identify, _ := json.Marshal(map[string]any{"op": 2, "d": map[string]any{
		"token":      token,
		"intents":    intents,
		"properties": map[string]string{"os": "linux", "browser": "stress-driver", "device": "stress-driver"},
	}})
	if err := wsutil.WriteClientText(conn, identify); err != nil {
		_ = conn.Close()
		return fmt.Errorf("send IDENTIFY: %w", err)
	}
	for {
		var f gatewayFrame
		if err := readJSON(rw, &f); err != nil {
			_ = conn.Close()
			return fmt.Errorf("await READY: %w", err)
		}
		if f.Op == 0 && f.T == "READY" {
			break
		}
	}

	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()
	// Heartbeats are the only writes after IDENTIFY, so no write lock.
	go func() {
		tick := time.NewTicker(time.Duration(max(hello.D.HeartbeatInterval, 1000)) * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if wsutil.WriteClientText(conn, []byte(`{"op":1,"d":null}`)) != nil {
					return
				}
			}
		}
	}()
	go func() {
		for {
			var f struct {
				Op int    `json:"op"`
				T  string `json:"t"`
				D  struct {
					Nonce string `json:"nonce"`
				} `json:"d"`
			}
			if err := readJSON(rw, &f); err != nil {
				if ctx.Err() == nil {
					logf("gateway %s: read: %v", url, err)
				}
				return
			}
			if f.T != "MESSAGE_CREATE" {
				continue
			}
			if s, ok := strings.CutPrefix(f.D.Nonce, noncePrefix); ok {
				if n, err := strconv.Atoi(s); err == nil {
					onEvent(n)
				}
			}
		}
	}()
	return nil
}

// readJSON reads the next data frame (answering pings on the way) and
// decodes it into v.
func readJSON(rw io.ReadWriter, v any) error {
	msg, _, err := wsutil.ReadServerData(rw)
	if err != nil {
		return err
	}
	return json.Unmarshal(msg, v)
}
