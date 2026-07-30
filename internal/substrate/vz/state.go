//go:build darwin

package vz

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sivchari/rask/internal/cluster"
)

// vmState is what the "rask __vm-host" process (vmhost.go) persists for
// the "rask create"/"rask delete" CLI processes to read: neither has this
// process in memory (Start's CLI invocation exits after the guest becomes
// ready; Delete is a separate invocation entirely), matching
// internal/substrate/hostproc's cross-process PID handoff pattern.
type vmState struct {
	// PID is the __vm-host process's own PID, for Stop/Delete to kill.
	PID int `json:"pid"`

	// HostPort and AgentHostPort are the host TCP ports
	// 127.0.0.1:<port> forwards to the guest's apiserver and control
	// agent respectively (clusterNetwork.HostPort/AgentHostPort),
	// written as soon as the network is up so Start can find them
	// without guessing.
	HostPort      int `json:"hostPort"`
	AgentHostPort int `json:"agentHostPort"`
}

func vmStatePath(homeDir, name string) string {
	return filepath.Join(cluster.Dir(homeDir, name), "data", "vm-state.json")
}

func writeVMState(path string, state vmState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("vz: encoding vm state: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("vz: creating %s: %w", filepath.Dir(path), err)
	}

	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o600); err != nil {
		return fmt.Errorf("vz: writing %s: %w", tmpPath, err)
	}

	// Atomic rename so a concurrent reader (Start polling for this file)
	// never observes a partially-written file.
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("vz: finalizing %s: %w", path, err)
	}

	return nil
}

func readVMState(path string) (vmState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return vmState{}, err
	}

	var state vmState
	if err := json.Unmarshal(data, &state); err != nil {
		return vmState{}, fmt.Errorf("vz: decoding %s: %w", path, err)
	}

	return state, nil
}
