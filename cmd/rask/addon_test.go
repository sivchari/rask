package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestAddonInstall_UnknownAddonRejects(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()

	root := newRootCommand(&fakeRuntime{}, homeDir)
	root.SetArgs([]string{"addon", "install", "bogus"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	err := root.Execute()
	if err == nil {
		t.Fatal("Execute() = nil error, want an unknown-addon error")
	}

	if !strings.Contains(err.Error(), "unknown addon") {
		t.Errorf("Execute() error = %v, want it to mention \"unknown addon\"", err)
	}
}

func TestAddonInstall_MissingArgRejects(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()

	root := newRootCommand(&fakeRuntime{}, homeDir)
	root.SetArgs([]string{"addon", "install"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err == nil {
		t.Fatal("Execute() = nil error, want an error for a missing addon name argument")
	}
}

func TestAddonInstall_ValidNameDispatchesPastValidation(t *testing.T) {
	t.Parallel()

	// No cluster named "dev" exists in this empty homeDir, so this must
	// fail while loading its kubeconfig — proving "gateway-api" passed
	// name validation and was dispatched to installAddon, rather than
	// being rejected as unknown.
	for _, addonName := range []string{"gateway-api", "snapshot-crds"} {
		t.Run(addonName, func(t *testing.T) {
			t.Parallel()

			homeDir := t.TempDir()

			root := newRootCommand(&fakeRuntime{}, homeDir)
			root.SetArgs([]string{"addon", "install", addonName, "--name", "dev"})
			root.SetOut(&bytes.Buffer{})
			root.SetErr(&bytes.Buffer{})

			err := root.Execute()
			if err == nil {
				t.Fatal("Execute() = nil error, want a kubeconfig-loading error (no such cluster in this test's homeDir)")
			}

			if strings.Contains(err.Error(), "unknown addon") {
				t.Errorf("Execute() error = %v, want a kubeconfig error, not an unknown-addon rejection", err)
			}
		})
	}
}
