// Package guestinit holds the pure-logic pieces of rask-init (the vz
// guest's PID 1 / bootstrap agent, cmd/rask-init): kernel module dependency
// resolution, kernel command-line parameter parsing, and binfmt_misc
// registration string construction. None of this touches the OS directly
// (no syscalls, no file I/O beyond what callers hand it as strings/bytes),
// so it builds and tests on any host, matching the pattern
// spikes/s3/init/{bootparam,binfmt} established.
package guestinit

import (
	"fmt"
	"path"
	"strings"
)

// ParseModulesDep parses a Linux modules.dep file (as produced by depmod),
// mapping each module's path — relative to the kernel's
// lib/modules/<release> directory, exactly as modules.dep itself writes it
// — to the paths of its direct dependencies.
func ParseModulesDep(content string) (map[string][]string, error) {
	deps := make(map[string][]string)

	for i, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		modPath, rest, ok := strings.Cut(line, ":")
		if !ok {
			return nil, fmt.Errorf("guestinit: modules.dep line %d: missing ':': %q", i+1, line)
		}

		deps[strings.TrimSpace(modPath)] = strings.Fields(rest)
	}

	return deps, nil
}

// ResolveLoadOrder returns every module in wanted (by bare name, e.g.
// "virtio_net", matched against modules.dep's basenames with modprobe's
// usual "-" / "_" interchangeability) together with its full transitive
// dependency closure, ordered so every dependency appears before whatever
// depends on it and each module appears exactly once — the order
// unix.InitModule must be called in.
func ResolveLoadOrder(deps map[string][]string, wanted []string) ([]string, error) {
	byName := make(map[string]string, len(deps))
	for modPath := range deps {
		byName[normalizeName(base(modPath))] = modPath
	}

	const (
		unvisited = iota
		visiting
		visited
	)

	state := make(map[string]int, len(deps))

	var order []string

	var visit func(modPath string) error

	visit = func(modPath string) error {
		switch state[modPath] {
		case visited:
			return nil
		case visiting:
			return fmt.Errorf("guestinit: dependency cycle detected at %s", modPath)
		}

		state[modPath] = visiting

		for _, dep := range deps[modPath] {
			if err := visit(dep); err != nil {
				return err
			}
		}

		state[modPath] = visited
		order = append(order, modPath)

		return nil
	}

	for _, w := range wanted {
		modPath, ok := byName[normalizeName(w)]
		if !ok {
			return nil, fmt.Errorf("guestinit: unknown module %q: no entry in modules.dep", w)
		}

		if err := visit(modPath); err != nil {
			return nil, err
		}
	}

	return order, nil
}

// base returns a modules.dep path's bare module name: the final path
// element with a ".ko.gz" or ".ko" suffix stripped.
func base(modPath string) string {
	b := path.Base(modPath)
	b = strings.TrimSuffix(b, ".gz")
	b = strings.TrimSuffix(b, ".ko")

	return b
}

// normalizeName makes "-" and "_" interchangeable in module names, matching
// modprobe/depmod's own module-name matching semantics (kernel source and
// modules.dep are not always consistent about which one a given module
// uses — e.g. "virtio-rng.ko.gz" on disk is loaded by the name
// "virtio_rng").
func normalizeName(name string) string {
	return strings.ReplaceAll(name, "-", "_")
}
