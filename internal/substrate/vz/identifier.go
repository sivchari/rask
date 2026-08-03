//go:build darwin

package vz

import (
	"fmt"
	"os"
	"path/filepath"

	cvz "github.com/Code-Hex/vz/v3"
)

// machineIdentifierFileName is where loadOrCreateMachineIdentifier persists
// a cluster's VZGenericMachineIdentifier, under the cluster's state
// directory.
const machineIdentifierFileName = "machine-identifier"

// loadOrCreateMachineIdentifier returns the persisted machine identifier at
// path, creating and persisting a new one if none exists yet.
//
// Found during the M0 s5 spike: RestoreMachineStateFromURL fails with
// VZErrorDomain Code=12 ("invalid argument") unless the restoring VM is
// configured with
// the exact same identifier as the VM that was saved. rask persists this
// per cluster at Create time — before v1 has any save/restore feature at
// all — because it's free to do now and expensive to retrofit once
// clusters already exist without one.
func loadOrCreateMachineIdentifier(path string) (*cvz.GenericMachineIdentifier, error) {
	data, err := loadOrCreateMachineIdentifierBytes(path)
	if err != nil {
		return nil, err
	}

	id, err := cvz.NewGenericMachineIdentifierWithData(data)
	if err != nil {
		return nil, fmt.Errorf("vz: parsing persisted machine identifier %s: %w", path, err)
	}

	return id, nil
}

// loadOrCreateMachineIdentifierBytes is loadOrCreateMachineIdentifier's
// file-handling core, split out so it's unit-testable without constructing
// a real vz.GenericMachineIdentifier.
func loadOrCreateMachineIdentifierBytes(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		return data, nil
	}

	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("vz: reading machine identifier %s: %w", path, err)
	}

	id, err := cvz.NewGenericMachineIdentifier()
	if err != nil {
		return nil, fmt.Errorf("vz: generating machine identifier: %w", err)
	}

	data = id.DataRepresentation()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("vz: creating %s: %w", filepath.Dir(path), err)
	}

	if err := os.WriteFile(path, data, 0o600); err != nil {
		return nil, fmt.Errorf("vz: writing machine identifier %s: %w", path, err)
	}

	return data, nil
}
