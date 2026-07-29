/*
Copyright 2026 The KCP Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package server

import (
	"context"
	"fmt"

	"github.com/spf13/pflag"

	genericapiserver "k8s.io/apiserver/pkg/server"
	kubeoptions "k8s.io/kubernetes/pkg/kubeapiserver/options"
)

// Authentication wraps BuiltInAuthenticationOptions, following the same
// pattern as kcp's front-proxy (pkg/proxy/options/authentication.go): reuse
// kube-apiserver's authentication machinery, but enable only the methods this
// component needs and override ApplyTo so unused auth plumbing is not dragged
// in.
//
// Why the VW authenticates callers itself rather than delegating:
type Authentication struct {
	BuiltInOptions *kubeoptions.BuiltInAuthenticationOptions
}

// NewAuthentication returns Authentication with the methods the access VW
// supports. Service accounts, token files and webhook authentication are
// deliberately left out; add them here if a deployment needs them.
func NewAuthentication() *Authentication {
	return &Authentication{
		BuiltInOptions: kubeoptions.NewBuiltInAuthenticationOptions().
			WithAnonymous().
			WithClientCert().
			WithOIDC().
			WithRequestHeader(),
	}
}

// AddFlags registers the authentication flags. WithOIDC contributes the
// --oidc-* family and --authentication-config; WithRequestHeader contributes
// --requestheader-*; WithClientCert contributes --client-ca-file.
func (c *Authentication) AddFlags(fs *pflag.FlagSet) {
	c.BuiltInOptions.AddFlags(fs)
}

// Validate delegates to the built-in options.
func (c *Authentication) Validate() []error {
	return c.BuiltInOptions.Validate()
}

// OIDCEnabled reports whether a JWT authenticator is configured, either
// through the structured authentication config or the --oidc-* flags.
func (c *Authentication) OIDCEnabled() bool {
	if c.BuiltInOptions == nil {
		return false
	}
	if c.BuiltInOptions.AuthenticationConfigFile != "" {
		return true
	}
	return c.BuiltInOptions.OIDC != nil && c.BuiltInOptions.OIDC.IssuerURL != ""
}

// RequestHeaderEnabled reports whether identity headers from a trusted proxy
// are accepted, which requires a requestheader client CA to verify that proxy.
func (c *Authentication) RequestHeaderEnabled() bool {
	return c.BuiltInOptions != nil &&
		c.BuiltInOptions.RequestHeader != nil &&
		c.BuiltInOptions.RequestHeader.ClientCAFile != ""
}

// ApplyTo builds the union authenticator and wires the CAs that must also be
// advertised on the serving side.
//
// BuiltInAuthenticationOptions.ApplyTo is intentionally not called: it expects
// kube-apiserver infrastructure (service account token getters, informers)
// this component does not have.
func (c *Authentication) ApplyTo(
	ctx context.Context,
	authenticationInfo *genericapiserver.AuthenticationInfo,
	servingInfo *genericapiserver.SecureServingInfo,
) error {
	authenticatorConfig, err := c.BuiltInOptions.ToAuthenticationConfig()
	if err != nil {
		return err
	}

	if authenticatorConfig.ClientCAContentProvider != nil {
		if err := authenticationInfo.ApplyClientCert(authenticatorConfig.ClientCAContentProvider, servingInfo); err != nil {
			return fmt.Errorf("unable to load client CA file: %w", err)
		}
	}

	// The requestheader CA has to be advertised in the TLS handshake as well,
	// not just used for verification. Without it the front-proxy finds no
	// acceptable CA matching its client certificate, sends none, the
	// requestheader authenticator never runs, and the forwarded identity
	// headers are dropped — which looks like an authorization failure rather
	// than a TLS configuration one.
	if authenticatorConfig.RequestHeaderConfig != nil && authenticatorConfig.RequestHeaderConfig.CAContentProvider != nil {
		if err := authenticationInfo.ApplyClientCert(authenticatorConfig.RequestHeaderConfig.CAContentProvider, servingInfo); err != nil {
			return fmt.Errorf("unable to load requestheader CA file: %w", err)
		}
	}

	authenticationInfo.APIAudiences = c.BuiltInOptions.APIAudiences

	authenticationInfo.Authenticator, _, _, _, err = authenticatorConfig.New(ctx)
	if err != nil {
		return fmt.Errorf("build authenticator: %w", err)
	}

	return nil
}
