// Package akrouter implements the ak-router routing strategy.
//
// The strategy currently selects exactly as the built-in fill-first strategy:
// Pick delegates to coreauth.FillFirstSelector, and FillFirstEquivalent tells
// the manager and scheduler to treat it as fill-first.
package akrouter

import (
	"context"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// Strategy is the routing.strategy value that selects ak-router.
const Strategy = "ak-router"

// Selector is the ak-router credential selector. It holds no state.
type Selector struct {
	ff coreauth.FillFirstSelector
}

// NewSelector returns an ak-router selector.
func NewSelector() *Selector { return &Selector{} }

// Pick selects a credential exactly as fill-first does.
func (s *Selector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*coreauth.Auth) (*coreauth.Auth, error) {
	return s.ff.Pick(ctx, provider, model, opts, auths)
}

// FillFirstEquivalent marks the selector as picking exactly like fill-first.
// Remove it when ak-router's pick differs from fill-first.
func (*Selector) FillFirstEquivalent() {}
