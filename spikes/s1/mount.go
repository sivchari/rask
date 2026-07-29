package main

import (
	"bufio"
	"os"
	"sort"
	"strings"
	"syscall"
)

// unmountUnder lazily unmounts every mount point nested under dir (e.g.
// overlayfs snapshots containerd leaves behind after creating a pod
// sandbox), deepest-first, so a subsequent os.RemoveAll doesn't fail with
// EBUSY. Best-effort: errors are ignored since dir is about to be deleted
// anyway.
func unmountUnder(dir string) {
	mounts, err := mountPointsUnder(dir)
	if err != nil {
		return
	}
	// Deepest paths first so nested mounts are detached before their
	// parents.
	sort.Slice(mounts, func(i, j int) bool { return len(mounts[i]) > len(mounts[j]) })
	for _, m := range mounts {
		_ = syscall.Unmount(m, syscall.MNT_DETACH)
	}
}

func mountPointsUnder(dir string) ([]string, error) {
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return nil, err
	}
	defer f.Close()

	prefix := strings.TrimRight(dir, "/") + "/"
	var mounts []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		mountPoint := fields[1]
		if strings.HasPrefix(mountPoint, prefix) {
			mounts = append(mounts, mountPoint)
		}
	}
	return mounts, scanner.Err()
}
