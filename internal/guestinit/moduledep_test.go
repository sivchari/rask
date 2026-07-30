package guestinit

import (
	"reflect"
	"testing"
)

const sampleModulesDep = `kernel/drivers/char/hw_random/rng-core.ko.gz:
kernel/drivers/char/hw_random/virtio-rng.ko.gz: kernel/drivers/char/hw_random/rng-core.ko.gz
kernel/net/packet/af_packet.ko.gz:
kernel/net/core/failover.ko.gz:
kernel/drivers/net/net_failover.ko.gz: kernel/drivers/net/veth.ko.gz kernel/net/core/failover.ko.gz
kernel/drivers/net/virtio_net.ko.gz: kernel/drivers/net/net_failover.ko.gz kernel/net/core/failover.ko.gz
kernel/drivers/net/veth.ko.gz:
kernel/fs/fuse/fuse.ko.gz:
kernel/fs/fuse/virtiofs.ko.gz: kernel/fs/fuse/fuse.ko.gz
kernel/fs/binfmt_misc.ko.gz:
kernel/fs/overlayfs/overlay.ko.gz:
`

func TestParseModulesDep(t *testing.T) {
	t.Parallel()

	deps, err := ParseModulesDep(sampleModulesDep)
	if err != nil {
		t.Fatalf("ParseModulesDep: %v", err)
	}

	got := deps["kernel/drivers/net/virtio_net.ko.gz"]
	want := []string{"kernel/drivers/net/net_failover.ko.gz", "kernel/net/core/failover.ko.gz"}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("deps[virtio_net] = %v, want %v", got, want)
	}

	if got := deps["kernel/net/packet/af_packet.ko.gz"]; len(got) != 0 {
		t.Errorf("deps[af_packet] = %v, want empty", got)
	}
}

func TestParseModulesDep_IgnoresBlankLines(t *testing.T) {
	t.Parallel()

	deps, err := ParseModulesDep("\n\nkernel/fs/binfmt_misc.ko.gz:\n\n")
	if err != nil {
		t.Fatalf("ParseModulesDep: %v", err)
	}

	if _, ok := deps["kernel/fs/binfmt_misc.ko.gz"]; !ok {
		t.Error("expected kernel/fs/binfmt_misc.ko.gz to be present")
	}
}

func TestResolveLoadOrder_DependenciesLoadBeforeDependents(t *testing.T) {
	t.Parallel()

	deps, err := ParseModulesDep(sampleModulesDep)
	if err != nil {
		t.Fatalf("ParseModulesDep: %v", err)
	}

	order, err := ResolveLoadOrder(deps, []string{"virtio_net", "virtiofs", "virtio-rng"})
	if err != nil {
		t.Fatalf("ResolveLoadOrder: %v", err)
	}

	pos := make(map[string]int, len(order))
	for i, p := range order {
		pos[p] = i
	}

	requireBefore := func(dep, dependent string) {
		t.Helper()

		depPath := findByBasename(t, order, dep)
		dependentPath := findByBasename(t, order, dependent)

		if pos[depPath] >= pos[dependentPath] {
			t.Errorf("%s (pos %d) did not load before %s (pos %d): order=%v", dep, pos[depPath], dependent, pos[dependentPath], order)
		}
	}

	requireBefore("rng-core", "virtio-rng")
	requireBefore("fuse", "virtiofs")
	requireBefore("net_failover", "virtio_net")
	requireBefore("failover", "virtio_net")
	requireBefore("veth", "net_failover")

	// Every module named in the wanted set (by basename) must appear
	// exactly once, even though virtio_net depends on failover both
	// directly and transitively (through net_failover).
	seen := map[string]int{}
	for _, p := range order {
		seen[p]++
	}

	for p, n := range seen {
		if n != 1 {
			t.Errorf("module %s appears %d times in load order, want exactly once", p, n)
		}
	}
}

func TestResolveLoadOrder_UnknownModuleFails(t *testing.T) {
	t.Parallel()

	deps, err := ParseModulesDep(sampleModulesDep)
	if err != nil {
		t.Fatalf("ParseModulesDep: %v", err)
	}

	if _, err := ResolveLoadOrder(deps, []string{"does-not-exist"}); err == nil {
		t.Fatal("ResolveLoadOrder with an unknown module = nil error, want error")
	}
}

func TestResolveLoadOrder_DetectsCycle(t *testing.T) {
	t.Parallel()

	deps := map[string][]string{
		"a.ko.gz": {"b.ko.gz"},
		"b.ko.gz": {"a.ko.gz"},
	}

	if _, err := ResolveLoadOrder(deps, []string{"a"}); err == nil {
		t.Fatal("ResolveLoadOrder with a dependency cycle = nil error, want error")
	}
}

func findByBasename(t *testing.T, paths []string, basename string) string {
	t.Helper()

	for _, p := range paths {
		if base(p) == basename || normalizeName(base(p)) == normalizeName(basename) {
			return p
		}
	}

	t.Fatalf("no entry with basename %q in %v", basename, paths)

	return ""
}
