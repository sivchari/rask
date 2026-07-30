package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
)

// ServiceAccountKeyPair is the bare (non-CA-signed) ECDSA key pair the API
// server uses to sign and verify Kubernetes service account tokens:
// PrivatePEM feeds --service-account-signing-key-file, PublicPEM feeds
// --service-account-key-file.
type ServiceAccountKeyPair struct {
	PrivatePEM []byte
	PublicPEM  []byte
}

// NewServiceAccountKeyPair generates a new ECDSA P-256 service account
// signing key pair.
func NewServiceAccountKeyPair() (*ServiceAccountKeyPair, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating service account key: %w", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshaling private key: %w", err)
	}

	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("marshaling public key: %w", err)
	}

	return &ServiceAccountKeyPair{
		PrivatePEM: pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
		PublicPEM:  pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}),
	}, nil
}
