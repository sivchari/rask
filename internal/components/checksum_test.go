package components

import "testing"

func TestParseChecksum(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		body     string
		filename string
		want     string
		wantErr  bool
	}{
		{
			name:     "bare hex, no filename (dl.k8s.io style)",
			body:     "1ca4bc7eb9b3d6f1e205da9cfab437c89d3760d0765a29a6bcbccf4ad51a2cb1\n",
			filename: "kubelet",
			want:     "1ca4bc7eb9b3d6f1e205da9cfab437c89d3760d0765a29a6bcbccf4ad51a2cb1",
		},
		{
			name:     "bare hex, no trailing newline",
			body:     "1ca4bc7eb9b3d6f1e205da9cfab437c89d3760d0765a29a6bcbccf4ad51a2cb1",
			filename: "kubelet",
			want:     "1ca4bc7eb9b3d6f1e205da9cfab437c89d3760d0765a29a6bcbccf4ad51a2cb1",
		},
		{
			name:     "hash + filename line (containerd/cni-plugins style)",
			body:     "9cc708f13c31588a2fbdbd13553d963356e990d50e9b95a2d6120f4ff2067910  containerd-2.3.3-linux-arm64.tar.gz\n",
			filename: "containerd-2.3.3-linux-arm64.tar.gz",
			want:     "9cc708f13c31588a2fbdbd13553d963356e990d50e9b95a2d6120f4ff2067910",
		},
		{
			name: "multiple hash+filename lines, PGP clearsign armor around them (runc style)",
			body: `-----BEGIN PGP SIGNED MESSAGE-----
Hash: SHA512

177df879d50c913eb205e898d5c1c05a18f574053c0ce5524c471208eaf06f6f  runc.amd64
ca70e7dbd6616ca782a59b5d3ac86909123fdaa9fa3f89dcf29051c70eee7ce9  runc.arm64
-----BEGIN PGP SIGNATURE-----

iKQEARYKAEw=
-----END PGP SIGNATURE-----
`,
			filename: "runc.arm64",
			want:     "ca70e7dbd6616ca782a59b5d3ac86909123fdaa9fa3f89dcf29051c70eee7ce9",
		},
		{
			name: "multiple sha256sum.txt lines, match by filename (kine style)",
			body: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa  kine-amd64\n" +
				"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb  kine-arm64\n",
			filename: "kine-arm64",
			want:     "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		},
		{
			name:     "no matching filename",
			body:     "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa  kine-amd64\n",
			filename: "kine-arm64",
			wantErr:  true,
		},
		{
			name:     "empty body",
			body:     "",
			filename: "kubelet",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseChecksum([]byte(tt.body), tt.filename)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseChecksum() error = %v, wantErr %v", err, tt.wantErr)
			}

			if err != nil {
				return
			}

			if got != tt.want {
				t.Errorf("parseChecksum() = %q, want %q", got, tt.want)
			}
		})
	}
}
