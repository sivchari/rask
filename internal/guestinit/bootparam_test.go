package guestinit

import "testing"

func TestParseBootParams_AllFieldsPresent(t *testing.T) {
	t.Parallel()

	cmdline := "console=hvc0 reboot=t panic=-1 rask.cluster=dev rask.boottime=1700000000000000000"

	got, err := ParseBootParams(cmdline)
	if err != nil {
		t.Fatalf("ParseBootParams: %v", err)
	}

	if got.ClusterName != "dev" {
		t.Errorf("ClusterName = %q, want %q", got.ClusterName, "dev")
	}

	if got.BootTimeUnixNano != 1700000000000000000 {
		t.Errorf("BootTimeUnixNano = %d, want %d", got.BootTimeUnixNano, 1700000000000000000)
	}
}

func TestParseBootParams_MissingClusterNameFails(t *testing.T) {
	t.Parallel()

	if _, err := ParseBootParams("console=hvc0 rask.boottime=123"); err == nil {
		t.Fatal("ParseBootParams without rask.cluster= = nil error, want error")
	}
}

func TestParseBootParams_MissingBoottimeFails(t *testing.T) {
	t.Parallel()

	if _, err := ParseBootParams("console=hvc0 rask.cluster=dev"); err == nil {
		t.Fatal("ParseBootParams without rask.boottime= = nil error, want error")
	}
}

func TestParseBootParams_InvalidBoottimeFails(t *testing.T) {
	t.Parallel()

	if _, err := ParseBootParams("rask.cluster=dev rask.boottime=not-a-number"); err == nil {
		t.Fatal("ParseBootParams with a non-numeric rask.boottime= = nil error, want error")
	}
}
