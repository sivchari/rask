package bootstrap_test

import (
	"testing"
	"time"

	"github.com/sivchari/rask/internal/bootstrap"
)

func TestTimeline_ElapsedMissingMarkIsFalse(t *testing.T) {
	t.Parallel()

	tl := bootstrap.NewTimeline(time.Now())

	if _, ok := tl.Elapsed("never_marked"); ok {
		t.Error("Elapsed(never_marked) ok = true, want false")
	}
}

func TestTimeline_MarkRecordsElapsedSinceT0(t *testing.T) {
	t.Parallel()

	t0 := time.Now().Add(-100 * time.Millisecond)
	tl := bootstrap.NewTimeline(t0)

	tl.Mark("kine_up")

	elapsed, ok := tl.Elapsed("kine_up")
	if !ok {
		t.Fatal("Elapsed(kine_up) ok = false, want true")
	}

	if elapsed < 90*time.Millisecond || elapsed > time.Second {
		t.Errorf("Elapsed(kine_up) = %v, want ~100ms", elapsed)
	}
}

func TestTimeline_MarkKeepsEarliestOnRepeat(t *testing.T) {
	t.Parallel()

	tl := bootstrap.NewTimeline(time.Now())

	tl.Mark("apiserver_readyz")
	first, _ := tl.Elapsed("apiserver_readyz")

	time.Sleep(10 * time.Millisecond)
	tl.Mark("apiserver_readyz")
	second, _ := tl.Elapsed("apiserver_readyz")

	if first != second {
		t.Errorf("second Mark() changed Elapsed: first=%v second=%v, want unchanged", first, second)
	}
}

func TestTimeline_TotalIsNodeReady(t *testing.T) {
	t.Parallel()

	tl := bootstrap.NewTimeline(time.Now())

	if _, ok := tl.Total(); ok {
		t.Error("Total() before node_ready is marked: ok = true, want false")
	}

	tl.Mark("node_ready")

	total, ok := tl.Total()
	if !ok {
		t.Fatal("Total() ok = false, want true")
	}

	nodeReady, _ := tl.Elapsed("node_ready")
	if total != nodeReady {
		t.Errorf("Total() = %v, want Elapsed(node_ready) = %v", total, nodeReady)
	}
}

func TestTimeline_BreakdownOrderedByCompletionTime(t *testing.T) {
	t.Parallel()

	tl := bootstrap.NewTimeline(time.Now())

	// containerd_up completes before kine_up despite kine_up appearing
	// first in PhaseNames' dependency order, exercising a parallel
	// branch racing ahead of a sequential one.
	tl.Mark("containerd_up")
	time.Sleep(5 * time.Millisecond)
	tl.Mark("kine_up")
	time.Sleep(5 * time.Millisecond)
	tl.Mark("apiserver_readyz")

	entries := tl.Breakdown()
	if len(entries) != 3 {
		t.Fatalf("len(Breakdown()) = %d, want 3", len(entries))
	}

	wantOrder := []string{"containerd_up", "kine_up", "apiserver_readyz"}
	for i, name := range wantOrder {
		if entries[i].Name != name {
			t.Errorf("entries[%d].Name = %q, want %q", i, entries[i].Name, name)
		}
	}

	if entries[0].SincePrev != entries[0].Elapsed {
		t.Errorf("first entry SincePrev = %v, want equal to Elapsed = %v", entries[0].SincePrev, entries[0].Elapsed)
	}

	for i := 1; i < len(entries); i++ {
		want := entries[i].Elapsed - entries[i-1].Elapsed
		if entries[i].SincePrev != want {
			t.Errorf("entries[%d].SincePrev = %v, want %v", i, entries[i].SincePrev, want)
		}
	}
}
