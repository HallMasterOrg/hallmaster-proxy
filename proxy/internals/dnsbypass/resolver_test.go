package dnsbypass

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// dnsStub is a tiny UDP DNS responder for tests. It answers every query with
// a single A record pointing at `answer`. It does not validate the query
// beyond echoing the transaction ID + question section.
type dnsStub struct {
	conn   *net.UDPConn
	addr   string
	answer net.IP
	wg     sync.WaitGroup
}

func newDNSStub(t *testing.T, answer net.IP) *dnsStub {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	s := &dnsStub{conn: conn, addr: conn.LocalAddr().String(), answer: answer.To4()}
	s.wg.Add(1)
	go s.serve()
	return s
}

func (s *dnsStub) Close() {
	s.conn.Close()
	s.wg.Wait()
}

func (s *dnsStub) serve() {
	defer s.wg.Done()
	buf := make([]byte, 1500)
	for {
		n, raddr, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		resp := buildResponse(buf[:n], s.answer)
		if resp != nil {
			_, _ = s.conn.WriteToUDP(resp, raddr)
		}
	}
}

// buildResponse takes a DNS query and returns a response with one A record
// pointing at `answer`. Returns nil if the query is malformed.
func buildResponse(query []byte, answer net.IP) []byte {
	if len(query) < 12 {
		return nil
	}
	// Skip the question name + qtype + qclass to find where it ends so we
	// can append the answer section.
	off := 12
	for off < len(query) {
		l := int(query[off])
		if l == 0 {
			off++
			break
		}
		if l&0xc0 != 0 { // compression pointer in a query is unusual but harmless
			off += 2
			break
		}
		off += l + 1
	}
	if off+4 > len(query) {
		return nil
	}
	questionEnd := off + 4 // qtype (2) + qclass (2)

	resp := make([]byte, 0, questionEnd+16)
	resp = append(resp, query[:questionEnd]...)

	// Flags: standard response, no error, recursion available.
	binary.BigEndian.PutUint16(resp[2:4], 0x8180)
	// ANCOUNT = 1.
	binary.BigEndian.PutUint16(resp[6:8], 1)

	// Answer section: name pointer 0xc00c -> offset 12.
	resp = append(resp, 0xc0, 0x0c)
	// TYPE A.
	resp = append(resp, 0x00, 0x01)
	// CLASS IN.
	resp = append(resp, 0x00, 0x01)
	// TTL 60.
	resp = append(resp, 0x00, 0x00, 0x00, 0x3c)
	// RDLENGTH 4.
	resp = append(resp, 0x00, 0x04)
	resp = append(resp, answer...)

	return resp
}

func TestExternalResolver_ResolvesHostPort(t *testing.T) {
	stub := newDNSStub(t, net.IPv4(192, 0, 2, 1))
	defer stub.Close()

	r := NewExternalResolver(stub.addr, 2*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	got, err := r.Resolve(ctx, "discord.com:443")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "192.0.2.1:443" {
		t.Fatalf("got %q, want 192.0.2.1:443", got)
	}
}

func TestExternalResolver_DefaultsPort443(t *testing.T) {
	stub := newDNSStub(t, net.IPv4(192, 0, 2, 2))
	defer stub.Close()

	r := NewExternalResolver(stub.addr, 2*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	got, err := r.Resolve(ctx, "discord.com")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !strings.HasSuffix(got, ":443") {
		t.Fatalf("expected default :443 suffix, got %q", got)
	}
}

// ResolverIface check: the production type satisfies the interface and is
// thus swappable in tests.
var _ Resolver = (*ExternalResolver)(nil)

// dummyResolver demonstrates the seam: a test-side stub returning a canned
// address without any network calls.
type dummyResolver struct{ addr string }

func (d dummyResolver) Resolve(_ context.Context, _ string) (string, error) {
	if d.addr == "" {
		return "", fmt.Errorf("no addr")
	}
	return d.addr, nil
}

func TestResolverInterfaceSwappable(t *testing.T) {
	var r Resolver = dummyResolver{addr: "10.0.0.1:443"}
	got, err := r.Resolve(context.Background(), "anything")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "10.0.0.1:443" {
		t.Fatalf("got %q", got)
	}
}
