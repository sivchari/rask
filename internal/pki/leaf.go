package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"net"
	"time"
)

// Cert is a leaf certificate signed by a CA, together with its private key,
// ready to be written to disk or embedded in a kubeconfig.
type Cert struct {
	CertPEM []byte
	KeyPEM  []byte
}

// IssueServer generates an ECDSA P-256 key pair and issues a TLS server
// certificate signed by ca, valid for the given DNS SANs and IP SANs.
func (ca *CA) IssueServer(commonName string, sans []string, ips []net.IP) (*Cert, error) {
	template := &x509.Certificate{
		Subject:     pkix.Name{CommonName: commonName},
		DNSNames:    sans,
		IPAddresses: ips,
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	return ca.issue(template)
}

// IssueClient generates an ECDSA P-256 key pair and issues a TLS client
// certificate signed by ca. orgs become the certificate's Subject
// Organization field, which is how Kubernetes maps a client certificate to
// group membership (e.g. "system:masters").
func (ca *CA) IssueClient(commonName string, orgs []string) (*Cert, error) {
	template := &x509.Certificate{
		Subject:     pkix.Name{CommonName: commonName, Organization: orgs},
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}

	return ca.issue(template)
}

// issue fills in the fields common to every leaf certificate (serial
// number, validity window, signing key) and signs template with ca.
func (ca *CA) issue(template *x509.Certificate) (*Cert, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating leaf private key: %w", err)
	}

	serialNumber, err := newSerialNumber()
	if err != nil {
		return nil, err
	}

	now := time.Now()
	template.SerialNumber = serialNumber
	template.NotBefore = now.Add(-clockSkew)
	template.NotAfter = now.Add(certValidity)

	certDER, err := x509.CreateCertificate(rand.Reader, template, ca.Cert, &key.PublicKey, ca.Key)
	if err != nil {
		return nil, fmt.Errorf("creating leaf certificate: %w", err)
	}

	keyPEM, err := encodeECKeyPEM(key)
	if err != nil {
		return nil, err
	}

	return &Cert{
		CertPEM: encodeCertPEM(certDER),
		KeyPEM:  keyPEM,
	}, nil
}
