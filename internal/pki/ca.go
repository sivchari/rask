package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"
)

const (
	// certValidity is how long a rask-generated CA or leaf certificate is
	// valid for. rask regenerates its PKI on every cluster create, so a
	// long validity avoids clock-related expiry surprises rather than
	// modeling real-world rotation.
	certValidity = 10 * 365 * 24 * time.Hour

	// clockSkew backdates NotBefore so certificates are already valid on
	// hosts whose clock is slightly behind the one that issued them.
	clockSkew = 5 * time.Minute

	// serialNumberBits bounds the random serial number generated for
	// each certificate.
	serialNumberBits = 128
)

// CA is a self-signed certificate authority used to issue TLS server and
// client certificates for a rask cluster.
type CA struct {
	Cert    *x509.Certificate
	Key     *ecdsa.PrivateKey
	CertPEM []byte
	KeyPEM  []byte
}

// NewCA generates a self-signed ECDSA P-256 certificate authority with the
// given common name. The returned CA is valid for 10 years and can issue
// leaf certificates via IssueServer and IssueClient.
func NewCA(commonName string) (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating CA private key: %w", err)
	}

	serialNumber, err := newSerialNumber()
	if err != nil {
		return nil, err
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          serialNumber,
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             now.Add(-clockSkew),
		NotAfter:              now.Add(certValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("creating self-signed CA certificate: %w", err)
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, fmt.Errorf("parsing generated CA certificate: %w", err)
	}

	keyPEM, err := encodeECKeyPEM(key)
	if err != nil {
		return nil, err
	}

	return &CA{
		Cert:    cert,
		Key:     key,
		CertPEM: encodeCertPEM(certDER),
		KeyPEM:  keyPEM,
	}, nil
}

// newSerialNumber generates a random certificate serial number.
func newSerialNumber() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), serialNumberBits)

	serialNumber, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("generating certificate serial number: %w", err)
	}

	return serialNumber, nil
}

// encodeCertPEM encodes a DER certificate as PEM.
func encodeCertPEM(certDER []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
}

// encodeECKeyPEM encodes an ECDSA private key as PEM.
func encodeECKeyPEM(key *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshaling EC private key: %w", err)
	}

	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), nil
}
