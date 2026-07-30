package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sivchari/rask/internal/cluster"
	"github.com/sivchari/rask/internal/substrate"
)

func newLoadCommand(rt substrate.Runtime, homeDir string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "load",
		Short: "Load container images into a cluster's image store",
	}

	cmd.AddCommand(newLoadDockerImageCommand(rt, homeDir))
	cmd.AddCommand(newLoadImageArchiveCommand(rt, homeDir))

	return cmd
}

func newLoadDockerImageCommand(rt substrate.Runtime, homeDir string) *cobra.Command {
	var name string

	cmd := &cobra.Command{
		Use:   "docker-image IMAGE [IMAGE...]",
		Short: "Load one or more images from the local Docker daemon into a cluster",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return loadDockerImages(cmd.Context(), rt, homeDir, name, args)
		},
	}

	cmd.Flags().StringVar(&name, "name", defaultClusterName, "cluster name")

	return cmd
}

func newLoadImageArchiveCommand(rt substrate.Runtime, homeDir string) *cobra.Command {
	var name string

	cmd := &cobra.Command{
		Use:   "image-archive PATH [PATH...]",
		Short: "Load one or more docker-save/OCI tar archives from disk into a cluster",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return loadImageArchives(cmd.Context(), rt, homeDir, name, args)
		},
	}

	cmd.Flags().StringVar(&name, "name", defaultClusterName, "cluster name")

	return cmd
}

// requireClusterExists rejects with a clear error before either command
// does any work (spawning "docker save", opening a file, or calling
// rt.LoadImages) against a cluster that was never created.
func requireClusterExists(homeDir, name string) error {
	exists, err := cluster.Exists(homeDir, name)
	if err != nil {
		return err
	}

	if !exists {
		return fmt.Errorf("cluster %q does not exist", name)
	}

	return nil
}

// loadDockerImages loads each of refs from the local Docker daemon into the
// named cluster, one "docker save" process at a time — never more than one
// export in flight, matching substrate.Runtime.LoadImages's contract (see
// its doc comment) and keeping load gentle on a single shared Docker
// daemon.
func loadDockerImages(ctx context.Context, rt substrate.Runtime, homeDir, name string, refs []string) error {
	if err := requireClusterExists(homeDir, name); err != nil {
		return err
	}

	for _, ref := range refs {
		if err := loadOneDockerImage(ctx, rt, name, ref); err != nil {
			return fmt.Errorf("loading %s into cluster %q: %w", ref, name, err)
		}
	}

	return nil
}

// loadOneDockerImage streams "docker save <ref>"'s stdout directly into
// rt.LoadImages, without ever buffering the whole archive to disk or memory
// first. If docker save itself fails (no daemon, no such image, ...), that
// error takes priority over whatever rt.LoadImages made of the resulting
// short/empty stream, since it's the actual root cause.
func loadOneDockerImage(ctx context.Context, rt substrate.Runtime, name, ref string) error {
	save := exec.CommandContext(ctx, "docker", "save", ref)

	stdout, err := save.StdoutPipe()
	if err != nil {
		return fmt.Errorf("piping docker save output: %w", err)
	}

	var stderr bytes.Buffer

	save.Stderr = &stderr

	if err := save.Start(); err != nil {
		return fmt.Errorf("starting docker save (is Docker installed and the daemon running?): %w", err)
	}

	loadErr := rt.LoadImages(ctx, name, []substrate.ImageSource{{Reference: ref, Stream: stdout}})
	waitErr := save.Wait()

	if waitErr != nil {
		return fmt.Errorf("docker save %s: %w (%s)", ref, waitErr, strings.TrimSpace(stderr.String()))
	}

	return loadErr
}

// loadImageArchives loads each of paths, a docker-save or OCI tar already
// on disk, into the named cluster — the entry point for nerdctl/podman
// users and anyone without a Docker daemon at all.
func loadImageArchives(ctx context.Context, rt substrate.Runtime, homeDir, name string, paths []string) error {
	if err := requireClusterExists(homeDir, name); err != nil {
		return err
	}

	for _, path := range paths {
		if err := loadOneImageArchive(ctx, rt, name, path); err != nil {
			return fmt.Errorf("loading %s into cluster %q: %w", path, name, err)
		}
	}

	return nil
}

func loadOneImageArchive(ctx context.Context, rt substrate.Runtime, name, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("opening %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	return rt.LoadImages(ctx, name, []substrate.ImageSource{{Reference: path, Stream: f}})
}
