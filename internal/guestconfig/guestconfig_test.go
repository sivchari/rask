package guestconfig

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestConfig_IsZero(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  Config
		want bool
	}{
		{"zero value", Config{}, true},
		{"extra apiserver args", Config{ExtraAPIServerArgs: []string{"a=b"}}, false},
		{"coredns image", Config{CoreDNSImage: "example.com/coredns:v1"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := tt.cfg.IsZero(); got != tt.want {
				t.Errorf("IsZero() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMarshalLoad_RoundTrip(t *testing.T) {
	t.Parallel()

	want := Config{
		ExtraAPIServerArgs: []string{"authentication-token-webhook-config-file=/opt/rask/preboot/auth/webhook.yaml"},
		CoreDNSImage:       "example.com/coredns:v1",
	}

	data, err := Marshal(want)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	path := filepath.Join(t.TempDir(), "cluster-config.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("Load() = %+v, want %+v", got, want)
	}
}

func TestLoad_MissingFileReturnsZeroConfig(t *testing.T) {
	t.Parallel()

	got, err := Load(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if !got.IsZero() {
		t.Errorf("Load(missing file) = %+v, want a zero Config", got)
	}
}

func TestLoad_MalformedJSONErrors(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "cluster-config.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}

	if _, err := Load(path); err == nil {
		t.Error("Load(malformed JSON) = nil error, want an error")
	}
}
