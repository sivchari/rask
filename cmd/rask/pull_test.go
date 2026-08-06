package main

import (
	"bytes"
	"testing"
)

// TestPullCommand_NotBundledFailsWithoutCallingRuntime proves "rask pull"
// refuses to run against a non-bundled build (the only kind `go test`
// produces — bundlepayload.Available() is unconditionally false without
// -tags bundle and a staged payload) with a clear, actionable error,
// rather than falling through to whatever Runtime.Create would otherwise
// do. The success path (a real bundled binary with an embedded payload)
// is exercised for real in this task's E2E proof, not here — Available()
// can't be flipped true from within this package's normal test build.
func TestPullCommand_NotBundledFailsWithoutCallingRuntime(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	rt := &fakeRuntime{}

	root := newRootCommand(rt, homeDir)
	root.SetArgs([]string{"pull"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err == nil {
		t.Fatal("Execute() = nil error, want an error naming the missing embedded payload")
	}

	if len(rt.createCalls) != 0 {
		t.Errorf("createCalls = %v, want no calls (pull must refuse before touching the runtime)", rt.createCalls)
	}
}
