package main

import (
	"compress/gzip"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// bootModules are loaded in dependency order before anything that needs
// them (Rosetta's virtiofs share, binfmt_misc registration, virtio-net).
// The Alpine linux-virt kernel ships these as modules; the build script
// copies the .ko.gz files into the initramfs under /lib/modules/.
var bootModules = []string{
	"/lib/modules/rng-core.ko.gz",
	"/lib/modules/virtio-rng.ko.gz",
	"/lib/modules/af_packet.ko.gz",
	"/lib/modules/failover.ko.gz",
	"/lib/modules/net_failover.ko.gz",
	"/lib/modules/virtio_net.ko.gz",
	"/lib/modules/fuse.ko.gz",
	"/lib/modules/virtiofs.ko.gz",
	"/lib/modules/binfmt_misc.ko.gz",
	"/lib/modules/overlay.ko.gz",
}

func loadBootModules() error {
	for _, path := range bootModules {
		if err := loadModule(path); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	return nil
}

func loadModule(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	img, err := io.ReadAll(zr)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	if err := unix.InitModule(img, ""); err != nil && err != unix.EEXIST {
		return fmt.Errorf("init_module: %w", err)
	}
	return nil
}
