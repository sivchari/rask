package pki_test

import (
	"crypto/x509"
	"encoding/pem"
	"net"
	"path/filepath"
	"testing"
	"time"

	"k8s.io/client-go/tools/clientcmd"

	"github.com/sivchari/rask/internal/pki"
)

func TestNewCA_SelfSignedIsCAAndPEMRoundTrips(t *testing.T) {
	t.Parallel()

	ca, err := pki.NewCA("rask-ca")
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}

	if !ca.Cert.IsCA {
		t.Error("Cert.IsCA = false, want true")
	}

	pool := x509.NewCertPool()
	pool.AddCert(ca.Cert)

	if _, err := ca.Cert.Verify(x509.VerifyOptions{Roots: pool}); err != nil {
		t.Errorf("self-signed CA failed verification: %v", err)
	}

	certBlock, _ := pem.Decode(ca.CertPEM)
	if certBlock == nil {
		t.Fatal("pem.Decode(CertPEM) returned nil block")
	}

	if _, err := x509.ParseCertificate(certBlock.Bytes); err != nil {
		t.Errorf("x509.ParseCertificate(CertPEM): %v", err)
	}

	keyBlock, _ := pem.Decode(ca.KeyPEM)
	if keyBlock == nil {
		t.Fatal("pem.Decode(KeyPEM) returned nil block")
	}

	if _, err := x509.ParseECPrivateKey(keyBlock.Bytes); err != nil {
		t.Errorf("x509.ParseECPrivateKey(KeyPEM): %v", err)
	}
}

func TestNewCA_ExpiryIsTenYears(t *testing.T) {
	t.Parallel()

	ca, err := pki.NewCA("rask-ca")
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}

	const tenYears = 10 * 365 * 24 * time.Hour

	validity := ca.Cert.NotAfter.Sub(ca.Cert.NotBefore)

	// Allow slack for the clock-skew backdating applied to NotBefore.
	if validity < tenYears || validity > tenYears+24*time.Hour {
		t.Errorf("CA validity = %v, want ~%v", validity, tenYears)
	}
}

func TestIssueServer_SANsVerify(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cn   string
		sans []string
		ips  []net.IP
	}{
		{
			name: "dns names only",
			cn:   "rask-apiserver",
			sans: []string{"kubernetes", "kubernetes.default", "kubernetes.default.svc.cluster.local"},
		},
		{
			name: "dns and ip sans",
			cn:   "rask-apiserver",
			sans: []string{"rask-apiserver"},
			ips:  []net.IP{net.ParseIP("10.96.0.1"), net.ParseIP("127.0.0.1")},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ca, err := pki.NewCA("rask-ca")
			if err != nil {
				t.Fatalf("NewCA: %v", err)
			}

			serverCert, err := ca.IssueServer(tt.cn, tt.sans, tt.ips)
			if err != nil {
				t.Fatalf("IssueServer: %v", err)
			}

			certBlock, _ := pem.Decode(serverCert.CertPEM)
			if certBlock == nil {
				t.Fatal("pem.Decode(CertPEM) returned nil block")
			}

			cert, err := x509.ParseCertificate(certBlock.Bytes)
			if err != nil {
				t.Fatalf("x509.ParseCertificate(CertPEM): %v", err)
			}

			pool := x509.NewCertPool()
			pool.AddCert(ca.Cert)

			for _, name := range tt.sans {
				if _, err := cert.Verify(x509.VerifyOptions{DNSName: name, Roots: pool}); err != nil {
					t.Errorf("Verify(DNSName=%s): %v", name, err)
				}
			}

			for _, ip := range tt.ips {
				found := false

				for _, certIP := range cert.IPAddresses {
					if certIP.Equal(ip) {
						found = true

						break
					}
				}

				if !found {
					t.Errorf("IPAddresses missing %s, got %v", ip, cert.IPAddresses)
				}
			}
		})
	}
}

func TestIssueClient_Organizations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cn   string
		orgs []string
	}{
		{name: "no organizations", cn: "rask-admin", orgs: nil},
		{name: "system:masters", cn: "rask-admin", orgs: []string{"system:masters"}},
		{name: "multiple organizations", cn: "rask-user", orgs: []string{"system:masters", "rask:operators"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ca, err := pki.NewCA("rask-ca")
			if err != nil {
				t.Fatalf("NewCA: %v", err)
			}

			clientCert, err := ca.IssueClient(tt.cn, tt.orgs)
			if err != nil {
				t.Fatalf("IssueClient: %v", err)
			}

			certBlock, _ := pem.Decode(clientCert.CertPEM)
			if certBlock == nil {
				t.Fatal("pem.Decode(CertPEM) returned nil block")
			}

			cert, err := x509.ParseCertificate(certBlock.Bytes)
			if err != nil {
				t.Fatalf("x509.ParseCertificate(CertPEM): %v", err)
			}

			if cert.Subject.CommonName != tt.cn {
				t.Errorf("Subject.CommonName = %q, want %q", cert.Subject.CommonName, tt.cn)
			}

			// DER encodes Organization as a SET, so x509 may reorder
			// values by their encoded bytes; compare as a set rather
			// than by position.
			gotOrgs := make(map[string]bool, len(cert.Subject.Organization))
			for _, org := range cert.Subject.Organization {
				gotOrgs[org] = true
			}

			if len(cert.Subject.Organization) != len(tt.orgs) {
				t.Fatalf("Subject.Organization = %v, want %v", cert.Subject.Organization, tt.orgs)
			}

			for _, org := range tt.orgs {
				if !gotOrgs[org] {
					t.Errorf("Subject.Organization = %v, want to contain %q", cert.Subject.Organization, org)
				}
			}

			foundClientAuth := false

			for _, eku := range cert.ExtKeyUsage {
				if eku == x509.ExtKeyUsageClientAuth {
					foundClientAuth = true
				}
			}

			if !foundClientAuth {
				t.Errorf("ExtKeyUsage = %v, want ExtKeyUsageClientAuth", cert.ExtKeyUsage)
			}

			pool := x509.NewCertPool()
			pool.AddCert(ca.Cert)

			if _, err := cert.Verify(x509.VerifyOptions{
				Roots:     pool,
				KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
			}); err != nil {
				t.Errorf("Verify: %v", err)
			}
		})
	}
}

func TestWriteKubeconfig_RoundTrip(t *testing.T) {
	t.Parallel()

	ca, err := pki.NewCA("rask-ca")
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}

	clientCert, err := ca.IssueClient("rask-admin", []string{"system:masters"})
	if err != nil {
		t.Fatalf("IssueClient: %v", err)
	}

	path := filepath.Join(t.TempDir(), "kubeconfig")
	server := "https://127.0.0.1:6443"

	if err := pki.WriteKubeconfig(path, server, ca, clientCert, "rask", "rask-admin", "rask-rask"); err != nil {
		t.Fatalf("WriteKubeconfig: %v", err)
	}

	cfg, err := clientcmd.LoadFromFile(path)
	if err != nil {
		t.Fatalf("clientcmd.LoadFromFile: %v", err)
	}

	if cfg.CurrentContext != "rask-rask" {
		t.Errorf("CurrentContext = %q, want %q", cfg.CurrentContext, "rask-rask")
	}

	cluster, ok := cfg.Clusters["rask"]
	if !ok {
		t.Fatal(`Clusters["rask"] missing`)
	}

	if cluster.Server != server {
		t.Errorf("Cluster.Server = %q, want %q", cluster.Server, server)
	}

	if string(cluster.CertificateAuthorityData) != string(ca.CertPEM) {
		t.Error("Cluster.CertificateAuthorityData does not match ca.CertPEM")
	}

	authInfo, ok := cfg.AuthInfos["rask-admin"]
	if !ok {
		t.Fatal(`AuthInfos["rask-admin"] missing`)
	}

	if string(authInfo.ClientCertificateData) != string(clientCert.CertPEM) {
		t.Error("AuthInfo.ClientCertificateData does not match clientCert.CertPEM")
	}

	if string(authInfo.ClientKeyData) != string(clientCert.KeyPEM) {
		t.Error("AuthInfo.ClientKeyData does not match clientCert.KeyPEM")
	}

	context, ok := cfg.Contexts["rask-rask"]
	if !ok {
		t.Fatal(`Contexts["rask-rask"] missing`)
	}

	if context.Cluster != "rask" || context.AuthInfo != "rask-admin" {
		t.Errorf("Context = %+v, want Cluster=rask AuthInfo=rask-admin", context)
	}
}
