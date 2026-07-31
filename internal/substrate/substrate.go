// Package substrate defines the interface rask uses to run a cluster
// instance on a given host: a Virtualization.framework VM on macOS
// (internal/substrate/vz), or supervised host processes in a network
// namespace on Linux (internal/substrate/hostproc).
package substrate

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// StartOptions carries per-create-invocation settings for Runtime.Create
// and Runtime.Start that aren't part of a cluster's persisted identity.
// Every field here also has a "rask create" flag (see cmd/rask/create.go);
// this is the seam fjord (and any other CLI-shelling-out consumer) drives
// rask through.
type StartOptions struct {
	// ExtraAPIAudiences are additional kube-apiserver --api-audiences
	// values beyond the cluster's own service-account issuer, e.g. for a
	// TokenReview client (such as haro's projected ServiceAccount token)
	// that requests a custom audience.
	ExtraAPIAudiences []string

	// SeedPath, if set, is a prebaked datastore snapshot (see
	// internal/prebake) to seed the cluster's datastore from before boot,
	// skipping the cluster bootstrap reconciliation and default manifest
	// applies the seed already contains.
	SeedPath string

	// ExtraAPIServerArgs are additional "key=value" kube-apiserver flags
	// (kubeadm-style, no leading "--"; e.g.
	// "authentication-token-webhook-config-file=/path/to/kubeconfig"),
	// appended to the flags rask itself sets. A key naming one of rask's
	// own managed flags is rejected with an error rather than silently
	// overridden — see internal/bootstrap's buildAPIServerArgs for the
	// exact collision semantics and why.
	ExtraAPIServerArgs []string

	// PrebootFiles are copied into the cluster's data directory (see
	// PrebootSubdir's doc comment for the exact layout) before any
	// cluster process starts, so their destination path can be referenced
	// by an ExtraAPIServerArgs value (e.g. an authentication webhook
	// kubeconfig or a TLS certificate fjord wants kube-apiserver to read
	// at startup).
	PrebootFiles []PrebootFile

	// ComponentDir, if set, overrides rask's default dl.k8s.io download
	// cache for kube-apiserver/kube-controller-manager/kube-scheduler/
	// kubelet/kubectl with pre-extracted binaries from this local
	// directory (see internal/components.LocalDirSource). Every other
	// component (kube-proxy, kine, containerd, runc, the CNI plugins)
	// still comes from the default download cache.
	ComponentDir string

	// CoreDNSImage, if set, overrides manifests.CoreDNSImage for this
	// cluster's CoreDNS Deployment.
	CoreDNSImage string
}

// PrebootFile is a single file, copied from the host filesystem into a
// cluster's data directory before any cluster process starts (see
// StartOptions.PrebootFiles and PrebootSubdir).
type PrebootFile struct {
	// Src is a path on the filesystem of the process calling
	// Runtime.Start (e.g. the "rask create" invocation), readable at
	// Start time.
	Src string

	// Dest is a slash-separated path, relative to the cluster's preboot
	// directory (see PrebootSubdir), that Src's content is copied to. It
	// may not contain ".." segments or be absolute.
	Dest string
}

// PrebootSubdir is the fixed, cluster-data-directory-relative directory
// StartOptions.PrebootFiles are staged into, before any cluster process
// starts. A caller building an ExtraAPIServerArgs/--apiserver-arg value
// that references a preboot file's destination computes its absolute path
// as:
//
//	filepath.Join(dataDir, PrebootSubdir, dest)
//
// where dataDir is the cluster's data directory
// (<homeDir>/clusters/<name>/data on the hostproc substrate — see
// internal/cluster.Dir — or the vz substrate's fixed in-guest
// guestlayout.GuestAgentDataDir, since a vz cluster's data directory is not
// a host-readable path). For the default host home directory this is, e.g.,
// ~/.rask/clusters/<name>/data/preboot/<dest>.
const PrebootSubdir = "preboot"

// StagePrebootFiles copies every PrebootFile's Src content to
// filepath.Join(dataDir, PrebootSubdir, Dest), creating parent directories
// as needed. It is a no-op if files is empty.
//
// dest is validated against path traversal (a ".." segment or an absolute
// path): unlike every other path StartOptions carries, Dest is caller
// input that ends up interpolated into a filesystem path, so this is a
// real external-input boundary check, not a hypothetical one.
func StagePrebootFiles(dataDir string, files []PrebootFile) error {
	if len(files) == 0 {
		return nil
	}

	prebootDir := filepath.Join(dataDir, PrebootSubdir)

	for _, f := range files {
		dest, err := prebootDestPath(prebootDir, f.Dest)
		if err != nil {
			return err
		}

		data, err := os.ReadFile(f.Src)
		if err != nil {
			return fmt.Errorf("substrate: reading preboot file src %s: %w", f.Src, err)
		}

		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return fmt.Errorf("substrate: creating %s: %w", filepath.Dir(dest), err)
		}

		if err := os.WriteFile(dest, data, 0o600); err != nil {
			return fmt.Errorf("substrate: writing preboot file %s: %w", dest, err)
		}
	}

	return nil
}

// prebootDestPath resolves dest against prebootDir, rejecting a dest that
// would escape it.
func prebootDestPath(prebootDir, dest string) (string, error) {
	if dest == "" {
		return "", fmt.Errorf("substrate: preboot file has an empty dest")
	}

	if filepath.IsAbs(dest) || strings.Contains(filepath.ToSlash(dest), "../") || filepath.ToSlash(dest) == ".." {
		return "", fmt.Errorf("substrate: preboot file dest %q must be a relative path with no \"..\" segments", dest)
	}

	resolved := filepath.Join(prebootDir, filepath.FromSlash(dest))

	cleanPrebootDir := filepath.Clean(prebootDir)
	if resolved != cleanPrebootDir && !strings.HasPrefix(resolved, cleanPrebootDir+string(os.PathSeparator)) {
		return "", fmt.Errorf("substrate: preboot file dest %q escapes %s", dest, prebootDir)
	}

	return resolved, nil
}

// ImageSource is a single container image, as an OCI/docker archive tar
// stream (e.g. the output of "docker save"), to import into a cluster's
// image store via Runtime.LoadImages.
type ImageSource struct {
	// Reference identifies the image for progress/error reporting only;
	// LoadImages does not use it to select what gets imported — the
	// archive in Stream already carries its own name(s)/tag(s).
	Reference string

	// Stream is the archive's content. LoadImages reads it to exhaustion
	// exactly once and does not close it; the caller retains ownership of
	// its lifetime.
	Stream io.Reader
}

// Runtime creates, controls and tears down one rask cluster instance on the
// host. Every method after Create takes the cluster name Create was called
// with.
//
// Implementations are not required to be safe for concurrent use by
// multiple goroutines operating on the same cluster name.
type Runtime interface {
	// Create prepares a cluster instance (e.g. a VM disk image or a
	// network namespace) but does not start it. opts is the same
	// StartOptions later passed to Start; Create only ever consults its
	// ComponentDir field (to decide whether it needs to warm rask's
	// default download cache at all), never any of the others.
	Create(ctx context.Context, name string, opts StartOptions) error

	// Start boots the cluster instance created by Create.
	Start(ctx context.Context, name string, opts StartOptions) error

	// Stop shuts the cluster instance down without deleting its state,
	// so a later Start can resume it.
	Stop(ctx context.Context, name string) error

	// Delete removes a cluster instance and all of its state. It is an
	// error to call Delete on a cluster that is still running.
	Delete(ctx context.Context, name string) error

	// Exec runs command with args inside the cluster instance, streaming
	// its combined stdout/stderr to stdout, and returns the command's
	// exit code.
	Exec(ctx context.Context, name string, stdout io.Writer, command string, args ...string) (exitCode int, err error)

	// WriteFile writes data to path inside the cluster instance,
	// creating parent directories as needed.
	WriteFile(ctx context.Context, name string, path string, data []byte) error

	// PortForward forwards localAddr on the host to remoteAddr inside
	// the cluster. localAddr may use port 0 to let the OS pick a free
	// port; the actually-bound address is returned so callers never need
	// the racy reserve-close-rebind pattern.
	// the cluster instance until ctx is canceled or the returned error
	// channel is read from.
	PortForward(ctx context.Context, name string, localAddr, remoteAddr string) (boundAddr string, errs <-chan error, err error)

	// LoadImages imports each of images into the cluster instance's image
	// store, so a pod can reference them with imagePullPolicy: Never
	// without the cluster ever needing registry access. Each ImageSource
	// is read exactly once — implementations must not re-read or re-send
	// an image's archive more than once (kind's "load docker-image"
	// resends its whole multi-image tarball once per requested image;
	// see kind#3063).
	LoadImages(ctx context.Context, name string, images []ImageSource) error
}
