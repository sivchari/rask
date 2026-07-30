package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"
)

const (
	containerdSocket = "/run/containerd/containerd.sock"
	containerdConfig = "/etc/containerd/config.toml"
	binDir           = "/bin"
	testImage        = "docker.io/library/busybox:latest"
)

// startContainerd launches containerd as a background child (it stays
// running for the rest of the guest's life; rask-init's SIGCHLD reaper
// takes care of it and of the shim processes it spawns and detaches).
func startContainerd() (*exec.Cmd, error) {
	cmd := exec.Command(binDir+"/containerd", "--config", containerdConfig)
	cmd.Env = []string{"PATH=" + binDir}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stdout
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start containerd: %w", err)
	}
	return cmd, nil
}

// waitForContainerdSocket polls for the gRPC socket containerd creates once
// it has finished loading its plugins and is ready to serve.
func waitForContainerdSocket(ctx context.Context) error {
	for {
		if _, err := os.Stat(containerdSocket); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("containerd socket %s did not appear: %w", containerdSocket, ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// ctr runs the ctr CLI against the local containerd socket and returns its
// combined output.
func ctr(ctx context.Context, args ...string) (string, error) {
	fullArgs := append([]string{"--address", containerdSocket}, args...)
	cmd := exec.CommandContext(ctx, binDir+"/ctr", fullArgs...)
	cmd.Env = []string{"PATH=" + binDir}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// pullTestImage pulls testImage for linux/amd64 explicitly -- the whole
// point of this spike is that the arm64 host/guest can fetch and run an
// amd64-only manifest via Rosetta, so the platform must never be inferred
// from the host.
func pullTestImage(ctx context.Context) (string, error) {
	return ctr(ctx, "images", "pull", "--platform", "linux/amd64", testImage)
}

// runTestContainer runs `uname -m` inside testImage. A successful run whose
// stdout contains "x86_64" is the spike's pass condition: the arm64 guest
// kernel executed an amd64 ELF entrypoint via the binfmt_misc/Rosetta path
// registered by registerBinfmt.
func runTestContainer(ctx context.Context, id string) (string, error) {
	// --no-pivot: the spike's rootfs is the initramfs (ramfs), where
	// pivot_root(2) always fails with EINVAL. Production rask boots from a
	// virtio-blk root, so this flag is spike-only.
	return ctr(ctx, "run", "--rm", "--no-pivot", "--platform", "linux/amd64", testImage, id, "uname", "-m")
}
