package main

import (
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"strings"
)

// debugDumpSysFs is a one-shot diagnostic for the cgroup-mount investigation
// in RESULTS.md: it lists /sys/fs and greps the kernel's own build config
// (if exposed at /proc/config.gz via CONFIG_IKCONFIG_PROC) for CONFIG_CGROUP
// lines, so we can tell "kernel lacks cgroup support" apart from "mount
// ordering bug" without guessing.
func debugDumpSysFs() {
	entries, err := os.ReadDir("/sys/fs")
	if err != nil {
		fmt.Printf("DEBUG /sys/fs: readdir: %v\n", err)
	} else {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		fmt.Printf("DEBUG /sys/fs entries: %v\n", names)
	}

	f, err := os.Open("/proc/config.gz")
	if err != nil {
		fmt.Printf("DEBUG /proc/config.gz: %v\n", err)
		return
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		fmt.Printf("DEBUG /proc/config.gz: gzip: %v\n", err)
		return
	}
	defer gz.Close()
	config, err := io.ReadAll(gz)
	if err != nil {
		fmt.Printf("DEBUG /proc/config.gz: read: %v\n", err)
		return
	}
	interesting := []string{
		"CONFIG_CGROUPS", "CONFIG_CGROUP_", "CONFIG_BINFMT_MISC",
		"CONFIG_OVERLAY_FS", "CONFIG_NAMESPACES", "CONFIG_UTS_NS",
		"CONFIG_IPC_NS", "CONFIG_PID_NS", "CONFIG_NET_NS", "CONFIG_USER_NS",
		"CONFIG_POSIX_MQUEUE",
	}
	for _, line := range strings.Split(string(config), "\n") {
		for _, key := range interesting {
			if strings.HasPrefix(line, key) || strings.HasPrefix(line, "# "+key) {
				fmt.Printf("DEBUG config: %s\n", line)
				break
			}
		}
	}
}
