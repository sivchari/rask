//go:build darwin

package vz

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/sivchari/rask/internal/components"
	"github.com/sivchari/rask/internal/guestinit"
	"github.com/sivchari/rask/internal/guestlayout"
	"github.com/sivchari/rask/internal/substrate/vz/embedded"
)

// templateInitramfsVersion is bumped whenever anything that changes the
// template initramfs's content changes (a new components version pin, a
// new guestinit.WantedModules entry, a new rask-init build) — the cached
// file at cacheDir/vz-initramfs-template-<version>.cpio is otherwise reused
// forever, so a version that silently went stale would boot a stale guest
// without any error.
const templateInitramfsVersion = "v12"

// buildTemplateInitramfs builds (or returns the cached path to) the
// initramfs shared by every rask cluster on this host: rask-init as /init,
// every Kubernetes/kine/runc/containerd/CNI binary
// (internal/components.Cache.Ensure) under guestlayout.BinDir/CNIBinDir,
// the guest kernel's modules.dep plus exactly the .ko.gz files
// guestinit.WantedModules resolves to (guestlayout.ModulesDir), the
// iptables and e2fsprogs userland bundles merged at the guest root, and a
// CA certificate bundle.
//
// Per-cluster state (only the cluster name and boot wall-clock time for v1
// — see runCommandLine) travels via the kernel command line, not a second,
// per-cluster cpio archive concatenated onto this one: everything else
// bootstrap.Boot needs (PKI, kubeconfigs, component configs) is generated
// fresh inside the guest at boot, exactly like internal/substrate/hostproc
// does on Linux. This is a deliberate simplification of the M0 planning
// notes' per-cluster config transport design (which anticipated shipping
// pre-generated PKI for future snapshot/restore stability): v1's goal is
// create/delete, not save/restore, and reusing
// internal/bootstrap.Boot completely unmodified is both simpler and
// consistent with hostproc's proven implementation.
func buildTemplateInitramfs(ctx context.Context, cache *components.Cache) (string, error) {
	if embedded.IsPlaceholder() {
		return "", fmt.Errorf("vz: internal/substrate/vz/embedded/rask-init is still the placeholder: run `make build-rask-init` first")
	}

	cachePath := filepath.Join(cache.Dir(), fmt.Sprintf("vz-initramfs-template-%s.cpio", templateInitramfsVersion))

	if _, err := os.Stat(cachePath); err == nil {
		return cachePath, nil
	}

	paths, err := cache.Ensure(ctx, components.DefaultK8sVersion, components.ARM64)
	if err != nil {
		return "", fmt.Errorf("vz: resolving component binaries: %w", err)
	}

	kernel, err := cache.EnsureGuestKernel(ctx)
	if err != nil {
		return "", fmt.Errorf("vz: resolving guest kernel: %w", err)
	}

	iptables, err := cache.EnsureIPTablesBundle(ctx)
	if err != nil {
		return "", fmt.Errorf("vz: resolving iptables bundle: %w", err)
	}

	e2fsprogs, err := cache.EnsureE2fsprogsBundle(ctx)
	if err != nil {
		return "", fmt.Errorf("vz: resolving e2fsprogs bundle: %w", err)
	}

	caBundlePath, err := cache.EnsureCABundle(ctx)
	if err != nil {
		return "", fmt.Errorf("vz: resolving CA bundle: %w", err)
	}

	gcompat, err := cache.EnsureGCompatBundle(ctx)
	if err != nil {
		return "", fmt.Errorf("vz: resolving gcompat bundle: %w", err)
	}

	busybox, err := cache.EnsureBusyboxBundle(ctx)
	if err != nil {
		return "", fmt.Errorf("vz: resolving busybox bundle: %w", err)
	}

	w := newCpioWriter()

	if err := w.WriteFile("init", 0o755, embedded.RaskInitBinary); err != nil {
		return "", fmt.Errorf("vz: writing /init: %w", err)
	}

	if err := addComponentBinaries(w, paths); err != nil {
		return "", err
	}

	if err := addGuestModules(w, kernel); err != nil {
		return "", err
	}

	if err := copyLocalTree(w, "", iptables.Dir); err != nil {
		return "", fmt.Errorf("vz: adding iptables bundle: %w", err)
	}

	if err := copyLocalTree(w, "", e2fsprogs.Dir); err != nil {
		return "", fmt.Errorf("vz: adding e2fsprogs bundle: %w", err)
	}

	if err := copyLocalTree(w, "", gcompat.Dir); err != nil {
		return "", fmt.Errorf("vz: adding gcompat bundle: %w", err)
	}

	if err := copyLocalTree(w, "", busybox.Dir); err != nil {
		return "", fmt.Errorf("vz: adding busybox bundle: %w", err)
	}

	caBundle, err := os.ReadFile(caBundlePath)
	if err != nil {
		return "", fmt.Errorf("vz: reading CA bundle: %w", err)
	}

	if err := w.WriteFile(strings.TrimPrefix(guestlayout.CACertPath, "/"), 0o644, caBundle); err != nil {
		return "", fmt.Errorf("vz: writing CA bundle: %w", err)
	}

	data, err := w.Finish()
	if err != nil {
		return "", fmt.Errorf("vz: finishing initramfs archive: %w", err)
	}

	tmpPath := cachePath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return "", fmt.Errorf("vz: writing %s: %w", tmpPath, err)
	}

	if err := os.Rename(tmpPath, cachePath); err != nil {
		return "", fmt.Errorf("vz: finalizing %s: %w", cachePath, err)
	}

	return cachePath, nil
}

// addComponentBinaries copies every resolved component binary into the
// initramfs at guestlayout.Paths()'s fixed locations. containerd's whole
// bin/ directory (not just the containerd binary itself) is copied so its
// shim binaries land alongside it, matching
// components.Paths.ContainerdBinDir's doc comment ("must be invoked from
// its own directory unmoved").
func addComponentBinaries(w *cpioWriter, paths *components.Paths) error {
	singleFiles := map[string]string{
		paths.KubeAPIServer:         "kube-apiserver",
		paths.KubeControllerManager: "kube-controller-manager",
		paths.KubeScheduler:         "kube-scheduler",
		paths.Kubelet:               "kubelet",
		paths.KubeProxy:             "kube-proxy",
		paths.Kubectl:               "kubectl",
		paths.Kine:                  "kine",
		paths.Runc:                  "runc",
	}

	for src, name := range singleFiles {
		if err := copyLocalFile(w, path.Join(strings.TrimPrefix(guestlayout.BinDir, "/"), name), src); err != nil {
			return fmt.Errorf("vz: adding %s: %w", name, err)
		}
	}

	if err := copyLocalTree(w, strings.TrimPrefix(guestlayout.BinDir, "/"), paths.ContainerdBinDir); err != nil {
		return fmt.Errorf("vz: adding containerd bin dir: %w", err)
	}

	if err := copyLocalTree(w, strings.TrimPrefix(guestlayout.CNIBinDir, "/"), paths.CNIBinDir); err != nil {
		return fmt.Errorf("vz: adding CNI plugins: %w", err)
	}

	return nil
}

// addGuestModules resolves guestinit.WantedModules against the guest
// kernel's real modules.dep and copies modules.dep plus exactly the
// resolved .ko.gz files into guestlayout.ModulesDir, preserving their
// relative paths (so modules.dep's own paths resolve directly against
// guestlayout.ModulesDir at boot — see loadModules in cmd/rask-init).
func addGuestModules(w *cpioWriter, kernel *components.GuestKernel) error {
	depContent, err := os.ReadFile(filepath.Join(kernel.ModulesDir, "modules.dep"))
	if err != nil {
		return fmt.Errorf("vz: reading modules.dep: %w", err)
	}

	deps, err := guestinit.ParseModulesDep(string(depContent))
	if err != nil {
		return fmt.Errorf("vz: parsing modules.dep: %w", err)
	}

	order, err := guestinit.ResolveLoadOrder(deps, guestinit.WantedModules)
	if err != nil {
		return fmt.Errorf("vz: resolving guest module load order: %w", err)
	}

	modulesPrefix := strings.TrimPrefix(guestlayout.ModulesDir, "/")

	if err := w.WriteFile(path.Join(modulesPrefix, "modules.dep"), 0o644, depContent); err != nil {
		return fmt.Errorf("vz: writing modules.dep: %w", err)
	}

	for _, modPath := range order {
		data, err := os.ReadFile(filepath.Join(kernel.ModulesDir, modPath))
		if err != nil {
			return fmt.Errorf("vz: reading module %s: %w", modPath, err)
		}

		if err := w.WriteFile(path.Join(modulesPrefix, modPath), 0o644, data); err != nil {
			return fmt.Errorf("vz: writing module %s: %w", modPath, err)
		}
	}

	return nil
}

// copyLocalFile adds a single regular file from the host filesystem at
// srcPath into the archive at guestPath, preserving its host executable
// bit.
func copyLocalFile(w *cpioWriter, guestPath, srcPath string) error {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("reading %s: %w", srcPath, err)
	}

	info, err := os.Stat(srcPath)
	if err != nil {
		return fmt.Errorf("stat %s: %w", srcPath, err)
	}

	return w.WriteFile(guestPath, uint32(info.Mode().Perm()), data)
}

// copyLocalTree walks the local directory srcDir and adds every regular
// file and symlink under it into the archive at guestPrefix/<relative
// path>. Writing the exact same path twice (e.g. the iptables and
// e2fsprogs bundles both shipping the same pinned musl shared library) is
// a no-op on the second write, not an error — see cpioWriter.WriteFile.
//
// If srcDir contains components.CaseCollisionManifest (written by
// extractTarGzPreserveSymlinks when a package ships two entries whose paths
// differ only by case — see that constant's doc comment for why this host's
// case-insensitive filesystem can't hold both directly), the disambiguated
// on-disk entries it lists are restored to their correct, case-sensitive
// guest paths instead of their on-disk ones.
func copyLocalTree(w *cpioWriter, guestPrefix, srcDir string) error {
	renames, manifestPath, err := loadCaseCollisionManifest(srcDir)
	if err != nil {
		return err
	}

	return filepath.WalkDir(srcDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if p == manifestPath {
			return nil
		}

		rel, err := filepath.Rel(srcDir, p)
		if err != nil {
			return err
		}

		if rel == "." {
			return nil
		}

		relSlash := filepath.ToSlash(rel)
		if real, ok := renames[relSlash]; ok {
			relSlash = real
		}

		guestPath := path.Join(guestPrefix, relSlash)

		switch {
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return fmt.Errorf("readlink %s: %w", p, err)
			}

			return w.WriteSymlink(guestPath, target)
		case d.IsDir():
			return nil // directories are created implicitly by WriteFile/WriteSymlink's ancestor handling
		default:
			info, err := d.Info()
			if err != nil {
				return err
			}

			if !info.Mode().IsRegular() {
				return nil
			}

			data, err := os.ReadFile(p)
			if err != nil {
				return fmt.Errorf("reading %s: %w", p, err)
			}

			return w.WriteFile(guestPath, uint32(info.Mode().Perm()), data)
		}
	})
}

// loadCaseCollisionManifest reads srcDir/components.CaseCollisionManifest,
// if present, into a map from a colliding entry's disambiguated on-disk
// relative path (slash-separated) to its correct guest relative path. It
// also returns the manifest file's own absolute path so copyLocalTree's
// walk can skip copying the bookkeeping file itself into the guest.
func loadCaseCollisionManifest(srcDir string) (map[string]string, string, error) {
	manifestPath := filepath.Join(srcDir, components.CaseCollisionManifest)

	data, err := os.ReadFile(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, manifestPath, nil
		}

		return nil, manifestPath, fmt.Errorf("reading %s: %w", manifestPath, err)
	}

	renames := map[string]string{}

	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}

		fields := strings.SplitN(line, "\t", 2)
		if len(fields) != 2 {
			return nil, manifestPath, fmt.Errorf("malformed case-collision manifest line %q in %s", line, manifestPath)
		}

		renames[fields[0]] = fields[1]
	}

	return renames, manifestPath, nil
}
