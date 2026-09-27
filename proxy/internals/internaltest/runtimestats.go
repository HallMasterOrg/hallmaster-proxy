package internaltest

import "runtime"

// RuntimeSnapshot captures the runtime metrics a benchmark cares about.
// Diff against a prior snapshot to report deltas via b.ReportMetric.
type RuntimeSnapshot struct {
	HeapAllocBytes uint64
	TotalAllocs    uint64
	Goroutines     int
	GCCount        uint32
}

// SnapshotRuntime forces a GC and reads MemStats. The forced GC is
// deliberate: benchmarks want a stable HeapAlloc baseline so the diff
// reflects per-op residue, not "did the runtime happen to GC mid-run."
// Callers measure at the start and end of the work-under-test.
func SnapshotRuntime() RuntimeSnapshot {
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return RuntimeSnapshot{
		HeapAllocBytes: ms.HeapAlloc,
		TotalAllocs:    ms.TotalAlloc,
		Goroutines:     runtime.NumGoroutine(),
		GCCount:        ms.NumGC,
	}
}

// Diff returns `b - a` component-wise. Goroutines may be negative if
// background goroutines exited during the window — callers should treat
// the goroutine delta as an upper bound on leaks, not an exact count.
type RuntimeDiff struct {
	HeapAllocBytes int64
	TotalAllocs    int64
	Goroutines     int
	GCCount        int32
}

// Sub returns `b - a`.
func (a RuntimeSnapshot) Sub(b RuntimeSnapshot) RuntimeDiff {
	return RuntimeDiff{
		HeapAllocBytes: int64(b.HeapAllocBytes) - int64(a.HeapAllocBytes),
		TotalAllocs:    int64(b.TotalAllocs) - int64(a.TotalAllocs),
		Goroutines:     b.Goroutines - a.Goroutines,
		GCCount:        int32(b.GCCount) - int32(a.GCCount),
	}
}
