package internaltest

import (
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// LatencyTamperer implements tamper.Tamperer for measurement-only use in
// benchmarks and the stress harness. Each hook records a wall-clock
// timestamp at entry; callers post-process the recorded slice to compute
// p50/p95/p99 over the durations they care about (request->response,
// inbound frame->outbound frame, etc.).
//
// All methods are safe under concurrent use. The hooks never block on
// allocation past the initial slice capacity — exceeding the capacity
// drops samples (incrementing Dropped) rather than growing the slice
// under load. Pre-size via NewLatencyTamperer.
type LatencyTamperer struct {
	mu       sync.Mutex
	samples  []Sample
	cap      int
	Dropped  atomic.Int64
	startRef time.Time
}

// Sample is one recorded hook entry.
type Sample struct {
	Kind Kind
	// At is nanoseconds since the LatencyTamperer was created.
	At int64
	// Bytes is the payload length seen by the hook (request body, decoded
	// response body, WS frame). Zero when not applicable.
	Bytes int
}

// Kind is the hook that produced a Sample.
type Kind uint8

const (
	KindRequest Kind = iota
	KindResponse
	KindWSIncoming
	KindWSOutgoing
)

// NewLatencyTamperer pre-allocates a sample buffer of `capacity` entries.
// Samples beyond the capacity are dropped (recorded in `Dropped`) — this
// keeps the hooks allocation-free in the steady state, which matters when
// the tamperer is on the proxy hot path.
func NewLatencyTamperer(capacity int) *LatencyTamperer {
	return &LatencyTamperer{
		samples:  make([]Sample, 0, capacity),
		cap:      capacity,
		startRef: time.Now(),
	}
}

func (l *LatencyTamperer) record(k Kind, n int) {
	now := time.Since(l.startRef).Nanoseconds()
	l.mu.Lock()
	if len(l.samples) >= l.cap {
		l.mu.Unlock()
		l.Dropped.Add(1)
		return
	}
	l.samples = append(l.samples, Sample{Kind: k, At: now, Bytes: n})
	l.mu.Unlock()
}

func (l *LatencyTamperer) Request(req *http.Request) (*http.Request, error) {
	n := 0
	if req != nil && req.ContentLength > 0 {
		n = int(req.ContentLength)
	}
	l.record(KindRequest, n)
	return req, nil
}

func (l *LatencyTamperer) Response(_ *http.Request, resp *http.Response, decodedBody []byte) (*http.Response, error) {
	l.record(KindResponse, len(decodedBody))
	return resp, nil
}

func (l *LatencyTamperer) WSIncoming(payload []byte) ([]byte, error) {
	l.record(KindWSIncoming, len(payload))
	return payload, nil
}

func (l *LatencyTamperer) WSOutgoing(payload []byte) ([]byte, error) {
	l.record(KindWSOutgoing, len(payload))
	return payload, nil
}

// Samples returns a copy of the recorded samples, in arrival order.
func (l *LatencyTamperer) Samples() []Sample {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Sample, len(l.samples))
	copy(out, l.samples)
	return out
}

// Pairs walks samples and returns the duration between each `enter` and
// the next `exit` kind. Used for proxy-overhead measurement — e.g. pair
// KindRequest -> KindResponse for HTTP round-trip times, or
// KindWSIncoming -> KindWSOutgoing on the gateway path.
//
// Samples that never receive a matching exit are skipped silently.
func (l *LatencyTamperer) Pairs(enter, exit Kind) []time.Duration {
	samples := l.Samples()
	durations := make([]time.Duration, 0, len(samples)/2)
	for i := 0; i < len(samples); i++ {
		if samples[i].Kind != enter {
			continue
		}
		for j := i + 1; j < len(samples); j++ {
			if samples[j].Kind == exit {
				durations = append(durations, time.Duration(samples[j].At-samples[i].At))
				i = j
				break
			}
		}
	}
	return durations
}

// Percentiles returns p50, p95, p99 of `durations`. Returns zeros for an
// empty slice. The input is sorted in place.
func Percentiles(durations []time.Duration) (p50, p95, p99 time.Duration) {
	if len(durations) == 0 {
		return 0, 0, 0
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	pick := func(p float64) time.Duration {
		idx := int(float64(len(durations)-1) * p)
		return durations[idx]
	}
	return pick(0.50), pick(0.95), pick(0.99)
}
