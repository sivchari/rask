package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// certValidity is generous since these PKI materials live only for the
// lifetime of a single spike run.
const certValidity = 10 * 365 * 24 * time.Hour

// clockSkew backdates NotBefore so certs are valid even if the VM clock is
// slightly behind the host clock that generated them.
const clockSkew = 5 * time.Minute

// pki holds every generated credential the spike needs, plus the
// kubeconfig files rendered from them.
type pki struct {
	dir string

	caCert *x509.Certificate
	caKey  *ecdsa.PrivateKey
	caPEM  []byte

	apiserverCertPEM []byte
	apiserverKeyPEM  []byte

	saPrivPEM []byte
	saPubPEM  []byte

	adminCertPEM []byte
	adminKeyPEM  []byte

	adminKubeconfig   string
	kubeletKubeconfig string
	cmKubeconfig      string
	schedKubeconfig   string
}

// buildPKI generates the CA, leaf certificates and kubeconfigs needed to
// stand up a single-node cluster and writes them under dir/pki and
// dir/kubeconfigs.
func buildPKI(dir, apiserverAdvertiseIP string) (*pki, error) {
	pkiDir := filepath.Join(dir, "pki")
	kcDir := filepath.Join(dir, "kubeconfigs")
	if err := os.MkdirAll(pkiDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating pki dir: %w", err)
	}
	if err := os.MkdirAll(kcDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating kubeconfigs dir: %w", err)
	}

	p := &pki{dir: dir}

	caCert, caKey, caPEM, caKeyPEM, err := newCA("rask-spike-s1-ca")
	if err != nil {
		return nil, fmt.Errorf("generating CA: %w", err)
	}
	p.caCert, p.caKey, p.caPEM = caCert, caKey, caPEM
	if err := writeFile(filepath.Join(pkiDir, "ca.crt"), caPEM); err != nil {
		return nil, err
	}
	if err := writeFile(filepath.Join(pkiDir, "ca.key"), caKeyPEM); err != nil {
		return nil, err
	}

	// apiserver serving cert.
	apiSANs := []string{"kubernetes", "kubernetes.default", "kubernetes.default.svc", "localhost"}
	apiIPs := []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("10.96.0.1")}
	if apiserverAdvertiseIP != "" {
		apiIPs = append(apiIPs, net.ParseIP(apiserverAdvertiseIP))
	}
	apiCertPEM, apiKeyPEM, err := issueServingCert(caCert, caKey, "kube-apiserver", apiSANs, apiIPs)
	if err != nil {
		return nil, fmt.Errorf("issuing apiserver serving cert: %w", err)
	}
	p.apiserverCertPEM, p.apiserverKeyPEM = apiCertPEM, apiKeyPEM
	if err := writeFile(filepath.Join(pkiDir, "apiserver.crt"), apiCertPEM); err != nil {
		return nil, err
	}
	if err := writeFile(filepath.Join(pkiDir, "apiserver.key"), apiKeyPEM); err != nil {
		return nil, err
	}

	// service account signing keypair (not CA-signed, just a bare keypair).
	saPrivPEM, saPubPEM, err := newServiceAccountKeyPair()
	if err != nil {
		return nil, fmt.Errorf("generating service-account keypair: %w", err)
	}
	p.saPrivPEM, p.saPubPEM = saPrivPEM, saPubPEM
	if err := writeFile(filepath.Join(pkiDir, "sa.key"), saPrivPEM); err != nil {
		return nil, err
	}
	if err := writeFile(filepath.Join(pkiDir, "sa.pub"), saPubPEM); err != nil {
		return nil, err
	}

	// admin client cert (O=system:masters).
	adminCertPEM, adminKeyPEM, err := issueClientCert(caCert, caKey, "admin", []string{"system:masters"})
	if err != nil {
		return nil, fmt.Errorf("issuing admin client cert: %w", err)
	}
	p.adminCertPEM, p.adminKeyPEM = adminCertPEM, adminKeyPEM
	if err := writeFile(filepath.Join(pkiDir, "admin.crt"), adminCertPEM); err != nil {
		return nil, err
	}
	if err := writeFile(filepath.Join(pkiDir, "admin.key"), adminKeyPEM); err != nil {
		return nil, err
	}

	// kubelet client cert. Node authorizer requires CN=system:node:<name>,
	// O=system:nodes.
	kubeletCertPEM, kubeletKeyPEM, err := issueClientCert(caCert, caKey, "system:node:"+nodeName, []string{"system:nodes"})
	if err != nil {
		return nil, fmt.Errorf("issuing kubelet client cert: %w", err)
	}
	if err := writeFile(filepath.Join(pkiDir, "kubelet.crt"), kubeletCertPEM); err != nil {
		return nil, err
	}
	if err := writeFile(filepath.Join(pkiDir, "kubelet.key"), kubeletKeyPEM); err != nil {
		return nil, err
	}

	// controller-manager client cert.
	cmCertPEM, cmKeyPEM, err := issueClientCert(caCert, caKey, "system:kube-controller-manager", nil)
	if err != nil {
		return nil, fmt.Errorf("issuing controller-manager client cert: %w", err)
	}

	// scheduler client cert.
	schedCertPEM, schedKeyPEM, err := issueClientCert(caCert, caKey, "system:kube-scheduler", nil)
	if err != nil {
		return nil, fmt.Errorf("issuing scheduler client cert: %w", err)
	}

	apiserverURL := fmt.Sprintf("https://127.0.0.1:%d", apiserverPort)

	p.adminKubeconfig = filepath.Join(kcDir, "admin.kubeconfig")
	if err := writeKubeconfig(p.adminKubeconfig, apiserverURL, caPEM, adminCertPEM, adminKeyPEM, "admin"); err != nil {
		return nil, err
	}
	p.kubeletKubeconfig = filepath.Join(kcDir, "kubelet.kubeconfig")
	if err := writeKubeconfig(p.kubeletKubeconfig, apiserverURL, caPEM, kubeletCertPEM, kubeletKeyPEM, "kubelet"); err != nil {
		return nil, err
	}
	p.cmKubeconfig = filepath.Join(kcDir, "controller-manager.kubeconfig")
	if err := writeKubeconfig(p.cmKubeconfig, apiserverURL, caPEM, cmCertPEM, cmKeyPEM, "controller-manager"); err != nil {
		return nil, err
	}
	p.schedKubeconfig = filepath.Join(kcDir, "scheduler.kubeconfig")
	if err := writeKubeconfig(p.schedKubeconfig, apiserverURL, caPEM, schedCertPEM, schedKeyPEM, "scheduler"); err != nil {
		return nil, err
	}

	return p, nil
}

func newCA(commonName string) (cert *x509.Certificate, key *ecdsa.PrivateKey, certPEM, keyPEM []byte, err error) {
	key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("generating CA key: %w", err)
	}

	serial, err := newSerialNumber()
	if err != nil {
		return nil, nil, nil, nil, err
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             now.Add(-clockSkew),
		NotAfter:              now.Add(certValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("creating self-signed CA cert: %w", err)
	}
	cert, err = x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("parsing CA cert: %w", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("marshaling CA key: %w", err)
	}

	return cert, key, encodeCertPEM(der), encodePrivateKeyPEM(keyDER), nil
}

func issueServingCert(caCert *x509.Certificate, caKey *ecdsa.PrivateKey, commonName string, dnsNames []string, ips []net.IP) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generating key: %w", err)
	}

	serial, err := newSerialNumber()
	if err != nil {
		return nil, nil, err
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: commonName},
		DNSNames:     dnsNames,
		IPAddresses:  ips,
		NotBefore:    now.Add(-clockSkew),
		NotAfter:     now.Add(certValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	der, err := x509.CreateCertificate(rand.Reader, template, caCert, &key.PublicKey, caKey)
	if err != nil {
		return nil, nil, fmt.Errorf("creating serving cert: %w", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("marshaling key: %w", err)
	}

	return encodeCertPEM(der), encodePrivateKeyPEM(keyDER), nil
}

func issueClientCert(caCert *x509.Certificate, caKey *ecdsa.PrivateKey, commonName string, orgs []string) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generating key: %w", err)
	}

	serial, err := newSerialNumber()
	if err != nil {
		return nil, nil, err
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: commonName, Organization: orgs},
		NotBefore:    now.Add(-clockSkew),
		NotAfter:     now.Add(certValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}

	der, err := x509.CreateCertificate(rand.Reader, template, caCert, &key.PublicKey, caKey)
	if err != nil {
		return nil, nil, fmt.Errorf("creating client cert: %w", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("marshaling key: %w", err)
	}

	return encodeCertPEM(der), encodePrivateKeyPEM(keyDER), nil
}

// newServiceAccountKeyPair generates the ECDSA keypair used for both
// --service-account-signing-key-file (private) and
// --service-account-key-file (public).
func newServiceAccountKeyPair() (privPEM, pubPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generating service-account key: %w", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("marshaling private key: %w", err)
	}

	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return nil, nil, fmt.Errorf("marshaling public key: %w", err)
	}

	return encodePrivateKeyPEM(keyDER), pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}), nil
}

func newSerialNumber() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("generating serial number: %w", err)
	}
	return serial, nil
}

func encodeCertPEM(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func encodePrivateKeyPEM(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
}

func writeFile(path string, data []byte) error {
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

const kubeconfigTemplate = `apiVersion: v1
kind: Config
clusters:
- name: rask-spike-s1
  cluster:
    server: %s
    certificate-authority-data: %s
contexts:
- name: %s
  context:
    cluster: rask-spike-s1
    user: %s
current-context: %s
users:
- name: %s
  user:
    client-certificate-data: %s
    client-key-data: %s
`

func writeKubeconfig(path, server string, caPEM, certPEM, keyPEM []byte, userName string) error {
	content := fmt.Sprintf(kubeconfigTemplate,
		server,
		base64Std(caPEM),
		userName, userName, userName,
		userName,
		base64Std(certPEM),
		base64Std(keyPEM),
	)
	return writeFile(path, []byte(content))
}
