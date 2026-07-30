package bootstrap

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"testing"

	"k8s.io/client-go/tools/clientcmd"

	"github.com/sivchari/rask/internal/cluster"
)

func TestGeneratePKI_APIServerCertVerifiesForEverySAN(t *testing.T) {
	t.Parallel()

	cpki, err := generatePKI(t.TempDir(), "10.0.0.5", "dev")
	if err != nil {
		t.Fatalf("generatePKI: %v", err)
	}

	certPEM, err := os.ReadFile(cpki.APIServerCertPath)
	if err != nil {
		t.Fatalf("reading apiserver cert: %v", err)
	}

	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("pem.Decode returned nil block")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}

	pool := x509.NewCertPool()
	pool.AddCert(cpki.CA.Cert)

	for _, dns := range []string{"kubernetes", "kubernetes.default", "kubernetes.default.svc", "localhost"} {
		if _, err := cert.Verify(x509.VerifyOptions{DNSName: dns, Roots: pool}); err != nil {
			t.Errorf("Verify(DNSName=%s): %v", dns, err)
		}
	}

	foundAdvertiseIP := false

	for _, ip := range cert.IPAddresses {
		if ip.String() == "10.0.0.5" {
			foundAdvertiseIP = true
		}
	}

	if !foundAdvertiseIP {
		t.Errorf("apiserver cert IPAddresses = %v, want to contain the advertise IP 10.0.0.5", cert.IPAddresses)
	}
}

func TestGeneratePKI_EveryKubeconfigLoadsAndAuthenticatesAsExpectedUser(t *testing.T) {
	t.Parallel()

	cpki, err := generatePKI(t.TempDir(), "", "dev")
	if err != nil {
		t.Fatalf("generatePKI: %v", err)
	}

	tests := []struct {
		path     string
		wantCN   string
		wantOrgs []string
	}{
		{cpki.AdminKubeconfigPath, "admin", []string{"system:masters"}},
		{cpki.KubeletKubeconfigPath, "system:node:" + cluster.NodeName, []string{"system:nodes"}},
		{cpki.ControllerManagerKubeconfigPath, "system:kube-controller-manager", nil},
		{cpki.SchedulerKubeconfigPath, "system:kube-scheduler", nil},
		{cpki.KubeProxyKubeconfigPath, "system:kube-proxy", nil},
	}

	for _, tt := range tests {
		cfg, err := clientcmd.LoadFromFile(tt.path)
		if err != nil {
			t.Fatalf("LoadFromFile(%s): %v", tt.path, err)
		}

		authInfo := cfg.AuthInfos[cfg.Contexts[cfg.CurrentContext].AuthInfo]

		block, _ := pem.Decode(authInfo.ClientCertificateData)
		if block == nil {
			t.Fatalf("%s: pem.Decode(ClientCertificateData) returned nil block", tt.path)
		}

		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			t.Fatalf("%s: ParseCertificate: %v", tt.path, err)
		}

		if cert.Subject.CommonName != tt.wantCN {
			t.Errorf("%s: CommonName = %q, want %q", tt.path, cert.Subject.CommonName, tt.wantCN)
		}

		if len(cert.Subject.Organization) != len(tt.wantOrgs) {
			t.Errorf("%s: Organization = %v, want %v", tt.path, cert.Subject.Organization, tt.wantOrgs)

			continue
		}

		for i, org := range tt.wantOrgs {
			if cert.Subject.Organization[i] != org {
				t.Errorf("%s: Organization = %v, want %v", tt.path, cert.Subject.Organization, tt.wantOrgs)
			}
		}

		pool := x509.NewCertPool()
		pool.AddCert(cpki.CA.Cert)

		if _, err := cert.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
			t.Errorf("%s: client cert did not verify against the generated CA: %v", tt.path, err)
		}
	}
}

func TestGeneratePKI_ServiceAccountKeysWrittenAndConsistent(t *testing.T) {
	t.Parallel()

	cpki, err := generatePKI(t.TempDir(), "", "dev")
	if err != nil {
		t.Fatalf("generatePKI: %v", err)
	}

	priv, err := os.ReadFile(cpki.ServiceAccountPrivPath)
	if err != nil {
		t.Fatalf("reading sa private key: %v", err)
	}

	if block, _ := pem.Decode(priv); block == nil || block.Type != "EC PRIVATE KEY" {
		t.Errorf("ServiceAccountPrivPath does not contain a PEM-encoded EC PRIVATE KEY")
	}

	pub, err := os.ReadFile(cpki.ServiceAccountPubPath)
	if err != nil {
		t.Fatalf("reading sa public key: %v", err)
	}

	if block, _ := pem.Decode(pub); block == nil || block.Type != "PUBLIC KEY" {
		t.Errorf("ServiceAccountPubPath does not contain a PEM-encoded PUBLIC KEY")
	}
}

// TestGeneratePKI_CAKeyPathIsTheActualCAPrivateKey is a regression test:
// CAKeyPath used to not exist at all, and boot.go's
// --cluster-signing-key-file pointed at CACertPath (the certificate, not a
// key) instead — which crashes kube-controller-manager's controller
// startup entirely, found via a real E2E run (see boot.go's comment at the
// CAKeyPath write site for the full story).
func TestGeneratePKI_CAKeyPathIsTheActualCAPrivateKey(t *testing.T) {
	t.Parallel()

	cpki, err := generatePKI(t.TempDir(), "", "dev")
	if err != nil {
		t.Fatalf("generatePKI: %v", err)
	}

	if cpki.CAKeyPath == cpki.CACertPath {
		t.Fatal("CAKeyPath == CACertPath, want distinct files")
	}

	keyPEM, err := os.ReadFile(cpki.CAKeyPath)
	if err != nil {
		t.Fatalf("reading CA key: %v", err)
	}

	block, _ := pem.Decode(keyPEM)
	if block == nil || block.Type != "EC PRIVATE KEY" {
		t.Fatal("CAKeyPath does not contain a PEM-encoded EC PRIVATE KEY")
	}

	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		t.Fatalf("ParseECPrivateKey: %v", err)
	}

	if !key.PublicKey.Equal(cpki.CA.Key.Public()) {
		t.Error("CAKeyPath's key does not match cpki.CA.Key (the key actually used to sign every issued certificate)")
	}
}
