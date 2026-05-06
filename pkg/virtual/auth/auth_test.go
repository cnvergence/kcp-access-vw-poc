package auth_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cnvergence/kcp-access-vw/pkg/virtual/auth"
)

// ── HeaderResolver ──────────────────────────────────────────────────

func TestHeaderResolver_success(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set("X-Remote-User", "alice")
	r.Header.Add("X-Remote-Group", "eng")
	r.Header.Add("X-Remote-Group", "platform")

	id, err := auth.HeaderResolver{}.Resolve(context.Background(), r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id.Username != "alice" {
		t.Errorf("Username = %q, want alice", id.Username)
	}
	if len(id.Groups) != 2 || id.Groups[0] != "eng" || id.Groups[1] != "platform" {
		t.Errorf("Groups = %v, want [eng platform]", id.Groups)
	}
}

func TestHeaderResolver_missingUser(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", nil)

	_, err := auth.HeaderResolver{}.Resolve(context.Background(), r)
	if err == nil {
		t.Fatal("expected error for missing X-Remote-User")
	}
}

func TestHeaderResolver_noGroups(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set("X-Remote-User", "bob")

	id, err := auth.HeaderResolver{}.Resolve(context.Background(), r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id.Username != "bob" {
		t.Errorf("Username = %q, want bob", id.Username)
	}
	if len(id.Groups) != 0 {
		t.Errorf("Groups = %v, want empty", id.Groups)
	}
}

// ── CertResolver ────────────────────────────────────────────────────

// testCA generates a self-signed CA and a client cert signed by it.
func testCA(t *testing.T, cn string, orgs []string) (*x509.CertPool, *x509.Certificate) {
	t.Helper()

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}

	pool := x509.NewCertPool()
	pool.AddCert(caCert)

	// Client cert
	clientKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clientTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject: pkix.Name{
			CommonName:   cn,
			Organization: orgs,
		},
		NotBefore:   time.Now().Add(-time.Hour),
		NotAfter:    time.Now().Add(time.Hour),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	clientDER, err := x509.CreateCertificate(rand.Reader, clientTmpl, caCert, &clientKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	clientCert, err := x509.ParseCertificate(clientDER)
	if err != nil {
		t.Fatal(err)
	}

	return pool, clientCert
}

func TestCertResolver_success(t *testing.T) {
	pool, clientCert := testCA(t, "alice", []string{"eng", "platform"})

	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.TLS = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{clientCert},
	}

	resolver := &auth.CertResolver{CAPool: pool}
	id, err := resolver.Resolve(context.Background(), r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id.Username != "alice" {
		t.Errorf("Username = %q, want alice", id.Username)
	}
	if len(id.Groups) != 2 || id.Groups[0] != "eng" || id.Groups[1] != "platform" {
		t.Errorf("Groups = %v, want [eng platform]", id.Groups)
	}
}

func TestCertResolver_noTLS(t *testing.T) {
	pool, _ := testCA(t, "alice", nil)
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	// r.TLS is nil

	resolver := &auth.CertResolver{CAPool: pool}
	_, err := resolver.Resolve(context.Background(), r)
	if err == nil {
		t.Fatal("expected error for missing TLS")
	}
}

func TestCertResolver_untrustedCA(t *testing.T) {
	pool1, _ := testCA(t, "alice", nil)
	_, clientCert := testCA(t, "bob", nil) // different CA

	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.TLS = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{clientCert},
	}

	resolver := &auth.CertResolver{CAPool: pool1}
	_, err := resolver.Resolve(context.Background(), r)
	if err == nil {
		t.Fatal("expected error for untrusted cert")
	}
}

// ── ChainResolver ───────────────────────────────────────────────────

func TestChainResolver_firstWins(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set("X-Remote-User", "alice")

	chain := &auth.ChainResolver{
		Resolvers: []auth.Resolver{
			auth.HeaderResolver{},
		},
	}
	id, err := chain.Resolve(context.Background(), r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id.Username != "alice" {
		t.Errorf("Username = %q, want alice", id.Username)
	}
}

func TestChainResolver_fallsThrough(t *testing.T) {
	// CertResolver fails (no TLS), HeaderResolver succeeds
	pool, _ := testCA(t, "nobody", nil)

	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set("X-Remote-User", "bob")

	chain := &auth.ChainResolver{
		Resolvers: []auth.Resolver{
			&auth.CertResolver{CAPool: pool},
			auth.HeaderResolver{},
		},
	}
	id, err := chain.Resolve(context.Background(), r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id.Username != "bob" {
		t.Errorf("Username = %q, want bob", id.Username)
	}
}

func TestChainResolver_allFail(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	// no headers, no TLS, no token

	chain := &auth.ChainResolver{
		Resolvers: []auth.Resolver{
			auth.HeaderResolver{},
		},
	}
	_, err := chain.Resolve(context.Background(), r)
	if err == nil {
		t.Fatal("expected error when all resolvers fail")
	}
}

func TestChainResolver_empty(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	chain := &auth.ChainResolver{}
	_, err := chain.Resolve(context.Background(), r)
	if err == nil {
		t.Fatal("expected error for empty chain")
	}
}
