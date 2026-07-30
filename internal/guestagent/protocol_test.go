package guestagent

import (
	"encoding/json"
	"testing"
)

func TestExecRequest_JSONRoundTrip(t *testing.T) {
	t.Parallel()

	want := ExecRequest{Command: "/bin/true", Args: []string{"-a", "-b"}}

	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got ExecRequest
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.Command != want.Command || len(got.Args) != len(want.Args) {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}

func TestFilePath_EncodesAndDecodes(t *testing.T) {
	t.Parallel()

	tests := []string{"/etc/kubernetes/kubeconfig", "/a b/c", "/simple"}

	for _, path := range tests {
		encoded := EncodeFilePath(path)

		decoded, err := DecodeFilePath(encoded)
		if err != nil {
			t.Fatalf("DecodeFilePath(%q): %v", encoded, err)
		}

		if decoded != path {
			t.Errorf("round trip %q -> %q -> %q, want %q", path, encoded, decoded, path)
		}
	}
}
