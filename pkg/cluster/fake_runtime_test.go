package cluster

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/sivchari/rask/internal/substrate"
)

// fakeRuntime is a substrate.Runtime test double, mirroring
// cmd/rask/fake_runtime_test.go's, so Provider's own logic (seed-path
// resolution, state-directory lifecycle, wait behavior) can be tested
// without a real substrate.
type fakeRuntime struct {
	createErr error
	startErr  error
	stopErr   error
	deleteErr error

	// onStart, if set, runs during Start after recording the call and
	// before startErr is returned, e.g. to simulate a real substrate
	// writing a kubeconfig/timeline file as a side effect of Start.
	onStart func(name string) error

	loadImagesErr error

	createCalls      []string
	createOptsCalls  []substrate.StartOptions
	startCalls       []string
	startOptsCalls   []substrate.StartOptions
	prebootPathCalls []prebootPathCall
	stopCalls        []string
	deleteCalls      []string
	loadImagesCalls  []loadImagesCall
	portForwardCalls []portForwardCall

	// portForwardErrCh, if set, is returned verbatim by PortForward so a
	// test can assert Provider.PortForward returns rt's error channel
	// unchanged (by identity) rather than a copy or a new one.
	portForwardErrCh chan error
}

type prebootPathCall struct {
	name string
	dest string
}

type loadImagesCall struct {
	name       string
	references []string
}

type portForwardCall struct {
	name       string
	localAddr  string
	remoteAddr string
}

func (f *fakeRuntime) Create(_ context.Context, name string, opts substrate.StartOptions) error {
	f.createCalls = append(f.createCalls, name)
	f.createOptsCalls = append(f.createOptsCalls, opts)

	return f.createErr
}

func (f *fakeRuntime) Start(_ context.Context, name string, opts substrate.StartOptions) error {
	f.startCalls = append(f.startCalls, name)
	f.startOptsCalls = append(f.startOptsCalls, opts)

	if f.onStart != nil {
		if err := f.onStart(name); err != nil {
			return err
		}
	}

	return f.startErr
}

// PrebootPath records the call and returns a deterministic, obviously-fake
// path derived from name and dest, so a test can assert Provider.PrebootPath
// both forwards its arguments unchanged and returns rt's result verbatim.
func (f *fakeRuntime) PrebootPath(name, dest string) string {
	f.prebootPathCalls = append(f.prebootPathCalls, prebootPathCall{name: name, dest: dest})

	return "/fake-preboot/" + name + "/" + dest
}

func (f *fakeRuntime) Stop(_ context.Context, name string) error {
	f.stopCalls = append(f.stopCalls, name)

	return f.stopErr
}

func (f *fakeRuntime) Delete(_ context.Context, name string) error {
	f.deleteCalls = append(f.deleteCalls, name)

	return f.deleteErr
}

func (f *fakeRuntime) Exec(_ context.Context, _ string, _ io.Writer, _ string, _ ...string) (int, error) {
	return 0, errors.New("fakeRuntime: Exec not implemented")
}

func (f *fakeRuntime) WriteFile(_ context.Context, _ string, _ string, _ []byte) error {
	return errors.New("fakeRuntime: WriteFile not implemented")
}

// PortForward records the call and returns a deterministic, obviously-fake
// bound address and (if set) rt.portForwardErrCh, so a test can assert
// Provider.PortForward both forwards its arguments unchanged and returns
// rt's results verbatim.
func (f *fakeRuntime) PortForward(_ context.Context, name string, localAddr, remoteAddr string) (string, <-chan error, error) {
	f.portForwardCalls = append(f.portForwardCalls, portForwardCall{name: name, localAddr: localAddr, remoteAddr: remoteAddr})

	return "127.0.0.1:41000", f.portForwardErrCh, nil
}

func (f *fakeRuntime) LoadImages(_ context.Context, name string, images []substrate.ImageSource) error {
	references := make([]string, len(images))
	for i, img := range images {
		references[i] = img.Reference
	}

	f.loadImagesCalls = append(f.loadImagesCalls, loadImagesCall{name: name, references: references})

	return f.loadImagesErr
}

// writeFakeKubeconfig writes a minimal, valid kubeconfig for cluster name
// under homeDir, at the same path Provider.KubeConfigPath computes —
// standing in for what a real substrate's Start writes.
func writeFakeKubeconfig(homeDir, name string) error {
	path := filepath.Join(homeDir, "clusters", name, "kubeconfig")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	content := `apiVersion: v1
kind: Config
clusters:
- name: ` + name + `
  cluster:
    server: https://127.0.0.1:6443
current-context: ` + name + `
contexts:
- context:
    cluster: ` + name + `
    user: admin
  name: ` + name + `
users:
- name: admin
`

	return os.WriteFile(path, []byte(content), 0o600)
}
