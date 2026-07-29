package cluster_test

import (
	"testing"

	"github.com/sivchari/rask/internal/cluster"
)

func TestFormatContext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		format string
		clname string
		want   string
	}{
		{name: "rask default", format: "rask-{name}", clname: "dev", want: "rask-dev"},
		{name: "kind compat", format: "kind-{name}", clname: "dev", want: "kind-dev"},
		{name: "no placeholder", format: "fixed", clname: "dev", want: "fixed"},
		{name: "placeholder only", format: "{name}", clname: "dev", want: "dev"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := cluster.FormatContext(tt.format, tt.clname)
			if got != tt.want {
				t.Errorf("FormatContext(%q, %q) = %q, want %q", tt.format, tt.clname, got, tt.want)
			}
		})
	}
}
