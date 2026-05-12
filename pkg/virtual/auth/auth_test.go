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

func TestHeaderResolver(t *testing.T) {
	tests := []struct {
		name       string
		headers    map[string][]string
		wantUser   string
		wantGroups []string
		wantErr    bool
	}{
		{
			name:       "user with groups",
			headers:    map[string][]string{"X-Remote-User": {"alice"}, "X-Remote-Group": {"eng", "platform"}},
			wantUser:   "alice",
			wantGroups: []string{"eng", "platform"},
		},
		{
			name:    "missing user header",
			headers: nil,
			wantErr: true,
		},
		{
			name:       "user without groups",
			headers:    map[string][]string{"X-Remote-User": {"bob"}},
			wantUser:   "bob",
			wantGroups: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/", nil)
			for k, vs := range tt.headers {
				for _, v := range vs {
					r.Header.Add(k, v)
				}
			}

			id, err := auth.HeaderResolver{}.Resolve(context.Background(), r)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if id.Username != tt.wantUser {
				t.Errorf("Username = %q, want %q", id.Username, tt.wantUser)
			}
			if len(id.Groups) != len(tt.wantGroups) {
				t.Errorf("Groups = %v, want %v", id.Groups, tt.wantGroups)
			}
		})
	}
}

func TestCertResolver(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(t *testing.T) (*auth.CertResolver, *http.Request)
		wantUser string
		wantErr  bool
	}{
		{
			name: "valid client cert",
			setup: func(t *testing.T) (*auth.CertResolver, *http.Request) {
				pool, clientCert := testCA(t, "alice", []string{"eng", "platform"})
				r := httptest.NewRequest(http.MethodPost, "/", nil)
				r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{clientCert}}
				return &auth.CertResolver{CAPool: pool}, r
			},
			wantUser: "alice",
		},
		{
			name: "no TLS connection",
			setup: func(t *testing.T) (*auth.CertResolver, *http.Request) {
				pool, _ := testCA(t, "alice", nil)
				r := httptest.NewRequest(http.MethodPost, "/", nil)
				return &auth.CertResolver{CAPool: pool}, r
			},
			wantErr: true,
		},
		{
			name: "untrusted CA",
			setup: func(t *testing.T) (*auth.CertResolver, *http.Request) {
				pool1, _ := testCA(t, "alice", nil)
				_, clientCert := testCA(t, "bob", nil) // different CA
				r := httptest.NewRequest(http.MethodPost, "/", nil)
				r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{clientCert}}
				return &auth.CertResolver{CAPool: pool1}, r
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolver, r := tt.setup(t)
			id, err := resolver.Resolve(context.Background(), r)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if id.Username != tt.wantUser {
				t.Errorf("Username = %q, want %q", id.Username, tt.wantUser)
			}
		})
	}
}

func TestChainResolver(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(t *testing.T) (*auth.ChainResolver, *http.Request)
		wantUser string
		wantErr  bool
	}{
		{
			name: "first resolver wins",
			setup: func(t *testing.T) (*auth.ChainResolver, *http.Request) {
				r := httptest.NewRequest(http.MethodPost, "/", nil)
				r.Header.Set("X-Remote-User", "alice")
				return &auth.ChainResolver{Resolvers: []auth.Resolver{auth.HeaderResolver{}}}, r
			},
			wantUser: "alice",
		},
		{
			name: "falls through to next resolver",
			setup: func(t *testing.T) (*auth.ChainResolver, *http.Request) {
				pool, _ := testCA(t, "nobody", nil)
				r := httptest.NewRequest(http.MethodPost, "/", nil)
				r.Header.Set("X-Remote-User", "bob")
				chain := &auth.ChainResolver{
					Resolvers: []auth.Resolver{
						&auth.CertResolver{CAPool: pool}, // fails: no TLS
						auth.HeaderResolver{},             // succeeds
					},
				}
				return chain, r
			},
			wantUser: "bob",
		},
		{
			name: "all resolvers fail",
			setup: func(t *testing.T) (*auth.ChainResolver, *http.Request) {
				r := httptest.NewRequest(http.MethodPost, "/", nil)
				return &auth.ChainResolver{Resolvers: []auth.Resolver{auth.HeaderResolver{}}}, r
			},
			wantErr: true,
		},
		{
			name: "empty chain",
			setup: func(t *testing.T) (*auth.ChainResolver, *http.Request) {
				r := httptest.NewRequest(http.MethodPost, "/", nil)
				return &auth.ChainResolver{}, r
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chain, r := tt.setup(t)
			id, err := chain.Resolve(context.Background(), r)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if id.Username != tt.wantUser {
				t.Errorf("Username = %q, want %q", id.Username, tt.wantUser)
			}
		})
	}
}
