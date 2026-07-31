package bootstrap

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/sivchari/rask/internal/components"
)

// TestBootContainerd_TimesOutWhenSocketNeverAppears is a regression test
// for boot.go's readiness waits having no bounded deadline of their own
// (see test/benchmark/PROGRESS-incontainer.md): /bin/sleep starts
// successfully (so sup.Launch succeeds) but never opens a unix socket, so
// without a bound this would hang forever instead of failing fast with a
// phase-named error.
func TestBootContainerd_TimesOutWhenSocketNeverAppears(t *testing.T) {
	t.Parallel()

	cfg := Config{Paths: &components.Paths{Containerd: "/bin/sleep"}}
	sup := NewSupervisor()
	t.Cleanup(sup.Stop)

	tl := NewTimeline(time.Now())
	cpaths := &containerdPaths{socketPath: "/nonexistent/containerd.sock"}
	containerdReady := make(chan struct{})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	err := bootContainerd(ctx, ctx, cfg, tl, sup, cpaths, containerdReady, t.TempDir(), 50*time.Millisecond)

	if err == nil {
		t.Fatal("bootContainerd() = nil error, want a timeout error")
	}

	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("bootContainerd() took %s, want it bounded by its own short timeout, not the 5s test ctx", elapsed)
	}

	if !strings.Contains(err.Error(), "containerd") {
		t.Errorf("bootContainerd() error = %q, want it to name the phase (containerd)", err.Error())
	}

	select {
	case <-containerdReady:
		t.Error("containerdReady was closed despite bootContainerd() returning an error")
	default:
	}
}

// TestBootKubelet_TimesOutWhenHealthzNeverReady is a regression test for
// the exact gap the task that produced this fix cited directly
// (boot.go's kubelet readiness wait, formerly unbounded — see
// test/benchmark/PROGRESS-incontainer.md's trial 2 and trial 4, both of
// which hung "rask create" forever on a kubelet that started but never
// became healthy).
func TestBootKubelet_TimesOutWhenHealthzNeverReady(t *testing.T) {
	t.Parallel()

	cfg := Config{Paths: &components.Paths{Kubelet: "/bin/sleep"}, NodeIP: "127.0.0.1"}
	sup := NewSupervisor()
	t.Cleanup(sup.Stop)

	tl := NewTimeline(time.Now())
	kpaths := &kubeletPaths{configPath: "/dev/null", rootDir: t.TempDir(), certDir: t.TempDir()}
	cpki := &ClusterPKI{KubeletKubeconfigPath: "/dev/null"}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	err := bootKubelet(ctx, ctx, cfg, tl, sup, kpaths, cpki, t.TempDir(), 50*time.Millisecond)

	if err == nil {
		t.Fatal("bootKubelet() = nil error, want a timeout error")
	}

	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("bootKubelet() took %s, want it bounded by its own short timeout, not the 5s test ctx", elapsed)
	}

	if !strings.Contains(err.Error(), "kubelet") {
		t.Errorf("bootKubelet() error = %q, want it to name the phase (kubelet)", err.Error())
	}

	if _, ok := tl.Elapsed("kubelet_started"); ok {
		t.Error("Timeline recorded kubelet_started despite bootKubelet() returning an error")
	}
}

// TestWatchNodeReady_TimesOutWhenNodeNeverBecomesReady covers the
// watch-based readiness shape (distinct from the polled-HTTP shape
// bootKubelet/bootContainerd cover above) using client-go's fake
// clientset, which is why watchNodeReady's clientset parameter was
// widened from the concrete *kubernetes.Clientset to kubernetes.Interface.
func TestWatchNodeReady_TimesOutWhenNodeNeverBecomesReady(t *testing.T) {
	t.Parallel()

	// No Node object is ever created, so the watch never sees a
	// registration or Ready event; watchNodeReady must still return once
	// its own timeout elapses instead of blocking on the outer ctx.
	clientset := fake.NewClientset()
	tl := NewTimeline(time.Now())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	err := watchNodeReady(ctx, clientset, tl, 50*time.Millisecond)

	if err == nil {
		t.Fatal("watchNodeReady() = nil error, want a timeout error")
	}

	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("watchNodeReady() took %s, want it bounded by its own short timeout, not the 5s test ctx", elapsed)
	}
}

// TestWatchNodeReady_ReturnsNilOnceNodeBecomesReady is the happy-path
// counterpart, confirming the timeout wrapper doesn't break the normal
// case: a Node that's already Ready when the watch starts.
func TestWatchNodeReady_ReturnsNilOnceNodeBecomesReady(t *testing.T) {
	t.Parallel()

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "rask-node"},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
			},
		},
	}

	clientset := fake.NewClientset(node)
	tl := NewTimeline(time.Now())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := watchNodeReady(ctx, clientset, tl, time.Second); err != nil {
		t.Errorf("watchNodeReady() = %v, want nil", err)
	}

	if _, ok := tl.Elapsed("node_ready"); !ok {
		t.Error("Timeline did not record node_ready")
	}
}
