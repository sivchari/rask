package components

import (
	"bytes"
	"context"
	"os"
	"testing"
)

func TestEnsureCABundle_DownloadsAndVerifies(t *testing.T) {
	t.Parallel()

	c := NewCache(t.TempDir())

	path, err := c.EnsureCABundle(context.Background())
	if err != nil {
		t.Fatalf("EnsureCABundle: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading cached bundle: %v", err)
	}

	if !bytes.Contains(data, []byte("BEGIN CERTIFICATE")) {
		t.Errorf("cached CA bundle does not look like PEM data (len=%d)", len(data))
	}
}
