package main

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// sampler polls the proxy container once per second for the duration
// of a scenario and writes one numeric CSV row per sample. A single
// `docker exec` reads cgroup v2 + /proc counters; `docker stats` was
// dropped because it blocks ~2s per call and emits unparseable strings
// ("5.1MiB / 7.7GiB"). The driver runs with /var/run/docker.sock mounted.
type sampler struct {
	container string
	out       *csv.Writer
	start     time.Time
}

// probe prints, one per line (same order as sampleHeader): cgroup memory
// bytes, cumulative CPU usec, VmRSS kB, thread count, open FDs, eth0 rx
// bytes, eth0 tx bytes.
const probe = `cat /sys/fs/cgroup/memory.current
awk '/^usage_usec/{print $2}' /sys/fs/cgroup/cpu.stat
awk '/^VmRSS/{print $2}' /proc/1/status
awk '/^Threads/{print $2}' /proc/1/status
ls /proc/1/fd | wc -l
awk '/eth0:/{print $2; print $10}' /proc/1/net/dev`

var sampleHeader = []string{"t_s", "mem_bytes", "cpu_usec", "rss_kb", "threads", "fds", "net_rx_bytes", "net_tx_bytes", "err"}

func newSampler(container string, w *csv.Writer) *sampler {
	_ = w.Write(sampleHeader)
	return &sampler{container: container, out: w, start: time.Now()}
}

func (s *sampler) sampleOnce(ctx context.Context) {
	row := make([]string, len(sampleHeader))
	row[0] = fmt.Sprintf("%.3f", time.Since(s.start).Seconds())
	out, err := exec.CommandContext(ctx, "docker", "exec", s.container, "sh", "-c", probe).Output()
	if ctx.Err() != nil {
		return // killed by shutdown; run() takes a final sample instead
	}
	fields := strings.Fields(string(out))
	if err == nil && len(fields) != len(sampleHeader)-2 {
		err = fmt.Errorf("probe returned %d fields, want %d", len(fields), len(sampleHeader)-2)
	}
	if err != nil {
		row[len(row)-1] = err.Error()
	} else {
		copy(row[1:], fields)
	}
	_ = s.out.Write(row)
	s.out.Flush()
}

// run samples once per second until ctx is cancelled, then takes one
// final sample so even sub-second scenarios get a before/after pair.
func (s *sampler) run(ctx context.Context) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	s.sampleOnce(ctx) // t=0 baseline
	for {
		select {
		case <-ctx.Done():
			final, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			s.sampleOnce(final)
			return
		case <-tick.C:
			s.sampleOnce(ctx)
		}
	}
}

// openCSV opens (or creates) `path` for writing and returns a buffered
// CSV writer. The file is overwritten on every run; rotating per-scenario
// directories is the orchestrator's job.
func openCSV(path string) (*csv.Writer, *os.File, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, nil, err
	}
	return csv.NewWriter(f), f, nil
}

// captureProxyLogs writes the proxy container's stdout/stderr emitted
// since `since` to `path`. Must run BEFORE the orchestrator tears the
// stack down — once the container is gone the logs go with it. The driver
// counts the tamper.Logging records in it to prove traffic went through
// the proxy.
func captureProxyLogs(ctx context.Context, container string, since time.Time, path string) error {
	// RFC3339 nano keeps fractional-second precision; Docker accepts it.
	cmd := exec.CommandContext(ctx, "docker", "logs", "--since", since.Format(time.RFC3339Nano), container)
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	cmd.Stdout = f
	cmd.Stderr = f
	return cmd.Run()
}
