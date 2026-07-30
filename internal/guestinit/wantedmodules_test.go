package guestinit

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sivchari/rask/internal/components"
)

// TestWantedModules_ResolveAgainstRealKernel verifies every name in
// WantedModules actually exists (by ResolveLoadOrder's "-"/"_" matching
// rule) in the pinned Alpine guest kernel's real modules.dep — catching a
// typo'd or renamed module name here rather than as a boot-time failure
// inside a vz VM. Hits the real network (downloads the guest kernel
// package via internal/components), so it's gated the same way
// components' own manual-verify tests are.
func TestWantedModules_ResolveAgainstRealKernel(t *testing.T) {
	if os.Getenv("RASK_VERIFY_NETWORK") != "1" {
		t.Skip("set RASK_VERIFY_NETWORK=1 to run (hits the real network)")
	}

	c := components.NewCache(t.TempDir())

	kernel, err := c.EnsureGuestKernel(context.Background())
	if err != nil {
		t.Fatalf("EnsureGuestKernel: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(kernel.ModulesDir, "modules.dep"))
	if err != nil {
		t.Fatalf("reading modules.dep: %v", err)
	}

	deps, err := ParseModulesDep(string(content))
	if err != nil {
		t.Fatalf("ParseModulesDep: %v", err)
	}

	order, err := ResolveLoadOrder(deps, WantedModules)
	if err != nil {
		t.Fatalf("ResolveLoadOrder(WantedModules): %v", err)
	}

	if len(order) < len(WantedModules) {
		t.Errorf("resolved load order has %d entries, want at least %d (one per WantedModules entry)", len(order), len(WantedModules))
	}

	for _, modPath := range order {
		if _, err := os.Stat(filepath.Join(kernel.ModulesDir, modPath)); err != nil {
			t.Errorf("resolved module %s has no .ko.gz file on disk: %v", modPath, err)
		}
	}
}
