package main

import (
	"fmt"
	"io"
	"sort"
	"sync"
	"time"
)

// timeline records named milestones relative to a fixed t0 (the moment the
// first component process is launched; PKI generation and config
// rendering happen before t0 and are not counted against the <5s budget).
type timeline struct {
	mu    sync.Mutex
	t0    time.Time
	marks []timelineMark
}

type timelineMark struct {
	name string
	t    time.Time
}

func newTimeline(t0 time.Time) *timeline {
	return &timeline{t0: t0}
}

// mark records name at the current time. Safe for concurrent use since
// phases run in parallel goroutines.
func (tl *timeline) mark(name string) {
	tl.mu.Lock()
	defer tl.mu.Unlock()
	tl.marks = append(tl.marks, timelineMark{name: name, t: time.Now()})
}

// elapsed returns the time of the first mark named name relative to t0, or
// false if it was never recorded.
func (tl *timeline) elapsed(name string) (time.Duration, bool) {
	tl.mu.Lock()
	defer tl.mu.Unlock()
	for _, m := range tl.marks {
		if m.name == name {
			return m.t.Sub(tl.t0), true
		}
	}
	return 0, false
}

// orderedNames is the canonical phase order used for the printed
// breakdown; phases not recorded in a given run (e.g. the stretch pod
// phase when --with-pod is off) are skipped.
var orderedNames = []string{
	"kine_up",
	"apiserver_readyz",
	"cm_sched_started",
	"containerd_up",
	"kubelet_started",
	"node_registered",
	"node_ready",
	"pod_running",
}

// writeBreakdown prints a phase-by-phase table: cumulative elapsed time
// since t0, and the delta since the previously-completed phase in wall
// clock order. Phases run in parallel branches (e.g. containerd_up races
// kine_up/apiserver_readyz), so rows are sorted by completion time rather
// than the fixed dependency order in orderedNames — a fixed order would
// produce misleading negative deltas whenever a "later" named phase
// actually finishes first.
func (tl *timeline) writeBreakdown(w io.Writer) {
	type entry struct {
		name string
		d    time.Duration
	}
	var entries []entry
	for _, name := range orderedNames {
		if d, ok := tl.elapsed(name); ok {
			entries = append(entries, entry{name, d})
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].d < entries[j].d })

	var prev time.Duration
	fmt.Fprintf(w, "%-20s %10s %10s\n", "PHASE", "CUMULATIVE", "DELTA")
	for _, e := range entries {
		fmt.Fprintf(w, "%-20s %10s %10s\n", e.name, e.d.Round(time.Millisecond), (e.d - prev).Round(time.Millisecond))
		prev = e.d
	}
}

// total returns elapsed("node_ready"), the headline <5s metric.
func (tl *timeline) total() (time.Duration, bool) {
	return tl.elapsed("node_ready")
}
