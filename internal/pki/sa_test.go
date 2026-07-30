package pki_test

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"testing"

	"github.com/sivchari/rask/internal/pki"
)

func TestNewServiceAccountKeyPair_RoundTripsAndMatches(t *testing.T) {
	t.Parallel()

	kp, err := pki.NewServiceAccountKeyPair()
	if err != nil {
		t.Fatalf("NewServiceAccountKeyPair: %v", err)
	}

	privBlock, _ := pem.Decode(kp.PrivatePEM)
	if privBlock == nil {
		t.Fatal("pem.Decode(PrivatePEM) returned nil block")
	}

	privKey, err := x509.ParseECPrivateKey(privBlock.Bytes)
	if err != nil {
		t.Fatalf("x509.ParseECPrivateKey(PrivatePEM): %v", err)
	}

	pubBlock, _ := pem.Decode(kp.PublicPEM)
	if pubBlock == nil {
		t.Fatal("pem.Decode(PublicPEM) returned nil block")
	}

	pubAny, err := x509.ParsePKIXPublicKey(pubBlock.Bytes)
	if err != nil {
		t.Fatalf("x509.ParsePKIXPublicKey(PublicPEM): %v", err)
	}

	pubKey, ok := pubAny.(*ecdsa.PublicKey)
	if !ok {
		t.Fatalf("public key type = %T, want *ecdsa.PublicKey", pubAny)
	}

	if !privKey.PublicKey.Equal(pubKey) {
		t.Error("PublicPEM does not match the public half of PrivatePEM")
	}
}

func TestNewServiceAccountKeyPair_UniquePerCall(t *testing.T) {
	t.Parallel()

	a, err := pki.NewServiceAccountKeyPair()
	if err != nil {
		t.Fatalf("NewServiceAccountKeyPair: %v", err)
	}

	b, err := pki.NewServiceAccountKeyPair()
	if err != nil {
		t.Fatalf("NewServiceAccountKeyPair: %v", err)
	}

	if string(a.PrivatePEM) == string(b.PrivatePEM) {
		t.Error("two calls produced the same private key")
	}
}
