//go:build darwin

package vz

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sivchari/rask/internal/guestconfig"
	"github.com/sivchari/rask/internal/guestlayout"
	"github.com/sivchari/rask/internal/substrate"
)

func TestStageClusterConfig_NoOverridesStagesNothing(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()

	if err := stageClusterConfig(dataDir, substrate.StartOptions{}); err != nil {
		t.Fatalf("stageClusterConfig: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dataDir, clusterConfigFileName)); !os.IsNotExist(err) {
		t.Errorf("stageClusterConfig with no overrides wrote %s, want nothing staged", clusterConfigFileName)
	}
}

func TestStageClusterConfig_ThenLoadStagedClusterConfig_RoundTrips(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()

	opts := substrate.StartOptions{
		ExtraAPIServerArgs: []string{"authentication-token-webhook-config-file=/opt/rask/preboot/auth/webhook.yaml"},
		CoreDNSImage:       "example.com/coredns:v1",
	}

	if err := stageClusterConfig(dataDir, opts); err != nil {
		t.Fatalf("stageClusterConfig: %v", err)
	}

	got, err := loadStagedClusterConfig(dataDir)
	if err != nil {
		t.Fatalf("loadStagedClusterConfig: %v", err)
	}

	want := guestConfigFrom(opts)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("loadStagedClusterConfig() = %+v, want %+v", got, want)
	}
}

func TestLoadStagedClusterConfig_NothingStagedReturnsZeroConfig(t *testing.T) {
	t.Parallel()

	got, err := loadStagedClusterConfig(t.TempDir())
	if err != nil {
		t.Fatalf("loadStagedClusterConfig: %v", err)
	}

	if !got.IsZero() {
		t.Errorf("loadStagedClusterConfig(nothing staged) = %+v, want a zero Config", got)
	}
}

func TestBuildClusterConfigCpio_ZeroConfigReturnsEmptyArchive(t *testing.T) {
	t.Parallel()

	data, err := buildClusterConfigCpio(guestconfig.Config{})
	if err != nil {
		t.Fatalf("buildClusterConfigCpio: %v", err)
	}

	if len(data) != 0 {
		t.Errorf("buildClusterConfigCpio(zero Config) = %d bytes, want 0", len(data))
	}
}

func TestBuildClusterConfigCpio_WritesJSONAtGuestClusterConfigPath(t *testing.T) {
	t.Parallel()

	cfg := guestconfig.Config{
		ExtraAPIServerArgs: []string{"a=b"},
		CoreDNSImage:       "example.com/coredns:v1",
	}

	data, err := buildClusterConfigCpio(cfg)
	if err != nil {
		t.Fatalf("buildClusterConfigCpio: %v", err)
	}

	if len(data) == 0 {
		t.Fatal("buildClusterConfigCpio(non-zero Config) = empty archive, want non-empty")
	}

	extracted := extractArchiveForTest(t, data)

	guestRelPath := strings.TrimPrefix(guestlayout.ClusterConfigPath, "/")

	got, err := os.ReadFile(filepath.Join(extracted, guestRelPath))
	if err != nil {
		t.Fatalf("reading extracted cluster config: %v", err)
	}

	var decoded guestconfig.Config
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatalf("decoding extracted cluster config: %v", err)
	}

	if !reflect.DeepEqual(decoded, cfg) {
		t.Errorf("decoded cluster config = %+v, want %+v", decoded, cfg)
	}
}
