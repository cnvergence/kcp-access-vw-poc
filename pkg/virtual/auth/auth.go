// Package auth provides caller-identity resolution for the Access
// Virtual Workspace.
//
// The VW needs to know who is calling before it can answer "which
// clusters can I see?". Three authentication mechanisms are
// supported, matching the kcp ecosystem:
//
//   - Bearer tokens: resolved via kcp's TokenReview API. This is the
//     primary path for standalone deployments where no FrontProxy sits
//     in front of the VW.
//   - Client certificates: the caller's TLS client cert is verified
//     against a CA bundle. CN → username, O → groups. Standard
//     Kubernetes cert-auth semantics.
//   - FrontProxy headers: X-Remote-User / X-Remote-Group, injected by
//     kcp's FrontProxy after it has done its own authentication. Only
//     safe when FrontProxy is the sole ingress; gated behind an
//     explicit opt-in flag.
//
// Callers construct a ChainResolver with the mechanisms they want
// (in priority order) and hand it to the SCAR handler.
package auth

import (
	"context"
	"crypto/x509"
	"fmt"
	"net/http"
	"strings"

	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Identity holds the resolved identity of a caller.
type Identity struct {
	Username string
	Groups   []string
}

// Resolver resolves caller identity from an HTTP request.
//
// Implementations must be safe for concurrent use.
type Resolver interface {
	// Resolve inspects the request and returns the caller's identity.
	// Returning a non-nil error means this resolver could not
	// authenticate the request; ChainResolver will try the next one.
	// A nil error means authentication succeeded.
	Resolve(ctx context.Context, r *http.Request) (*Identity, error)
}

// ── TokenReviewResolver ─────────────────────────────────────────────

// TokenReviewResolver resolves identity by extracting a bearer token
// from the Authorization header and calling kcp's TokenReview API
// with admin/service-account credentials.
//
// This is the primary auth path for standalone deployments.
type TokenReviewResolver struct {
	// Client is a Kubernetes client with credentials that can create
	// TokenReview objects on the target kcp (typically the system
	// service account under which the access VW runs).
	Client kubernetes.Interface
}

// Resolve extracts the bearer token and calls TokenReview.
func (r *TokenReviewResolver) Resolve(ctx context.Context, req *http.Request) (*Identity, error) {
	token := extractBearerToken(req)
	if token == "" {
		return nil, fmt.Errorf("no bearer token in request")
	}

	tr, err := r.Client.AuthenticationV1().TokenReviews().Create(ctx, &authenticationv1.TokenReview{
		Spec: authenticationv1.TokenReviewSpec{Token: token},
	}, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("token review: %w", err)
	}
	if !tr.Status.Authenticated {
		return nil, fmt.Errorf("token not authenticated")
	}

	return &Identity{
		Username: tr.Status.User.Username,
		Groups:   tr.Status.User.Groups,
	}, nil
}

// ── CertResolver ────────────────────────────────────────────────────

// CertResolver resolves identity from the caller's TLS client
// certificate. The certificate must be verified against the
// configured CA pool. CN → username, O → groups, matching standard
// Kubernetes client-certificate authentication semantics.
type CertResolver struct {
	// CAPool is the set of trusted certificate authorities.
	CAPool *x509.CertPool
}

// Resolve extracts identity from a verified client certificate.
func (r *CertResolver) Resolve(_ context.Context, req *http.Request) (*Identity, error) {
	if req.TLS == nil || len(req.TLS.PeerCertificates) == 0 {
		return nil, fmt.Errorf("no client certificate presented")
	}

	cert := req.TLS.PeerCertificates[0]

	// Verify the cert chain against our CA pool.
	opts := x509.VerifyOptions{
		Roots:     r.CAPool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	if _, err := cert.Verify(opts); err != nil {
		return nil, fmt.Errorf("client certificate verification failed: %w", err)
	}

	return &Identity{
		Username: cert.Subject.CommonName,
		Groups:   cert.Subject.Organization,
	}, nil
}

// ── HeaderResolver ──────────────────────────────────────────────────

// HeaderResolver trusts X-Remote-User and X-Remote-Group headers as
// authoritative. This is the correct path when kcp's FrontProxy is
// the sole ingress and has already authenticated the caller. It is
// NOT safe without FrontProxy — an attacker can forge the headers.
//
// Gated behind an explicit opt-in flag in cmd/server so it's never
// enabled accidentally.
type HeaderResolver struct{}

// Resolve reads identity from FrontProxy headers.
func (HeaderResolver) Resolve(_ context.Context, r *http.Request) (*Identity, error) {
	user := r.Header.Get("X-Remote-User")
	if user == "" {
		return nil, fmt.Errorf("no X-Remote-User header")
	}
	return &Identity{
		Username: user,
		Groups:   r.Header.Values("X-Remote-Group"),
	}, nil
}

// ── ChainResolver ───────────────────────────────────────────────────

// ChainResolver tries a series of Resolvers in order and returns the
// first successful result. If all resolvers fail, the last error is
// returned (it's usually the most informative).
type ChainResolver struct {
	Resolvers []Resolver
}

// Resolve tries each child resolver in order.
func (c *ChainResolver) Resolve(ctx context.Context, r *http.Request) (*Identity, error) {
	var lastErr error
	for _, res := range c.Resolvers {
		id, err := res.Resolve(ctx, r)
		if err == nil {
			return id, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no auth resolvers configured")
	}
	return nil, lastErr
}

// ── helpers ─────────────────────────────────────────────────────────

// extractBearerToken pulls the bearer token from the Authorization
// header, or falls back to the "token" query parameter (useful for
// WebSocket upgrades where browsers can't set headers).
func extractBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	if t := r.URL.Query().Get("token"); t != "" {
		return t
	}
	return ""
}
