package bootstrap

import (
	"sort"
	"sync"
	"time"
)

// PhaseNames is the canonical, dependency order of every phase Boot can
// mark. Not every phase is necessarily marked in a given run (e.g.
// "coredns_ready" is only recorded when Config.WaitForCoreDNS is set).
var PhaseNames = []string{
	"kine_up",
	"apiserver_readyz",
	"cm_sched_started",
	"containerd_up",
	"kubelet_started",
	"kube_proxy_started",
	"node_registered",
	"node_ready",
	"coredns_ready",
}

// Timeline records named boot-phase milestones relative to a fixed t0 (the
// moment Boot starts launching processes). It is safe for concurrent use,
// since Boot's phases run in parallel goroutines.
type Timeline struct {
	mu    sync.Mutex
	t0    time.Time
	marks []timelineMark
}

type timelineMark struct {
	name string
	t    time.Time
}

// NewTimeline returns a Timeline whose phases are measured relative to t0.
func NewTimeline(t0 time.Time) *Timeline {
	return &Timeline{t0: t0}
}

// Mark records name at the current time. Calling Mark more than once for
// the same name keeps only the earliest.
func (tl *Timeline) Mark(name string) {
	tl.mu.Lock()
	defer tl.mu.Unlock()

	for _, m := range tl.marks {
		if m.name == name {
			return
		}
	}

	tl.marks = append(tl.marks, timelineMark{name: name, t: time.Now()})
}

// Elapsed returns the time of the mark named name relative to t0, or false
// if it was never recorded.
func (tl *Timeline) Elapsed(name string) (time.Duration, bool) {
	tl.mu.Lock()
	defer tl.mu.Unlock()

	for _, m := range tl.marks {
		if m.name == name {
			return m.t.Sub(tl.t0), true
		}
	}

	return 0, false
}

// Total returns Elapsed("node_ready"), the headline boot-latency metric.
func (tl *Timeline) Total() (time.Duration, bool) {
	return tl.Elapsed("node_ready")
}

// PhaseEntry is one row of a Timeline's phase breakdown, ordered by
// completion time.
type PhaseEntry struct {
	Name      string
	Elapsed   time.Duration
	SincePrev time.Duration
}

// Breakdown returns every recorded phase ordered by completion time
// (parallel branches mean the dependency order in PhaseNames is not
// necessarily the completion order), with each entry's delta since the
// previous one.
func (tl *Timeline) Breakdown() []PhaseEntry {
	tl.mu.Lock()
	marks := make([]timelineMark, len(tl.marks))
	copy(marks, tl.marks)
	tl.mu.Unlock()

	sort.Slice(marks, func(i, j int) bool { return marks[i].t.Before(marks[j].t) })

	entries := make([]PhaseEntry, len(marks))

	var prev time.Duration

	for i, m := range marks {
		elapsed := m.t.Sub(tl.t0)
		entries[i] = PhaseEntry{Name: m.name, Elapsed: elapsed, SincePrev: elapsed - prev}
		prev = elapsed
	}

	return entries
}
