//go:build linux

package main

import (
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"github.com/sivchari/rask/internal/guestinit"
	"github.com/sivchari/rask/internal/guestlayout"
)

// loadModules loads every module in guestinit.WantedModules, in dependency
// order, from the .ko.gz files internal/substrate/vz's initramfs builder
// placed under guestlayout.ModulesDir (mirroring the guest kernel's own
// lib/modules/<release> layout, so modules.dep's relative paths resolve
// directly).
//
// A single module's own init_module(2) call failing does not abort the
// rest of boot: it's logged (RASK-INIT-MODULE-FAILED) and loading
// continues with the next module. Some modules' own init() functions
// return failure for reasons that don't actually block their dependents
// from working — observed live: libcrc32c.ko's init calls
// crypto_alloc_shash("crc32c", ...), which internally calls
// request_module() to look for an accelerated implementation; with no
// modprobe binary in this initramfs that lookup fails with ENOENT, and
// libcrc32c's init propagates it as init_module's return value, even
// though nf_conntrack (the actual reason libcrc32c is in the dependency
// closure) works fine against the kernel's builtin generic CRC32
// implementation regardless. Matching plan-m0-spikes.md's explicit
// guidance ("if an entire iptables module family is missing, kube-proxy
// can also run in a degraded mode — prefer fixing modules" — i.e. tolerate
// partial module failures rather than treat any single one as fatal to the
// whole boot), only a structurally broken initramfs (missing/corrupt
// modules.dep) aborts boot here.
func loadModules() error {
	depContent, err := os.ReadFile(filepath.Join(guestlayout.ModulesDir, "modules.dep"))
	if err != nil {
		return fmt.Errorf("reading modules.dep: %w", err)
	}

	deps, err := guestinit.ParseModulesDep(string(depContent))
	if err != nil {
		return fmt.Errorf("parsing modules.dep: %w", err)
	}

	order, err := guestinit.ResolveLoadOrder(deps, guestinit.WantedModules)
	if err != nil {
		return fmt.Errorf("resolving module load order: %w", err)
	}

	for _, modPath := range order {
		if err := loadModule(filepath.Join(guestlayout.ModulesDir, modPath)); err != nil {
			fmt.Printf("RASK-INIT-MODULE-FAILED module=%s err=%v\n", modPath, err)
		}
	}

	return nil
}

// loadModule gzip-decompresses a .ko.gz file and loads it via init_module(2).
// EEXIST (already loaded — some modules in guestinit.WantedModules'
// dependency closure may already be compiled in or loaded as a dependency
// of an earlier entry) is not an error.
func loadModule(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	zr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	defer func() { _ = zr.Close() }()

	img, err := io.ReadAll(zr)
	if err != nil {
		return fmt.Errorf("reading module image: %w", err)
	}

	if err := unix.InitModule(img, ""); err != nil && err != unix.EEXIST {
		return fmt.Errorf("init_module: %w", err)
	}

	return nil
}
