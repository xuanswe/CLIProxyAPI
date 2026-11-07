package akrouter_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	akrouter "github.com/router-for-me/CLIProxyAPI/v8/akc/ak-router"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// The scheduler and manager recognize fill-first equivalents by this method.
var _ interface{ FillFirstEquivalent() } = (*akrouter.Selector)(nil)

var _ coreauth.Selector = (*akrouter.Selector)(nil)

func prio(id, priority string) *coreauth.Auth {
	return &coreauth.Auth{ID: id, Provider: "codex", Attributes: map[string]string{"priority": priority}}
}

// TestSelectorPickMatchesFillFirst runs each input through ak-router and
// fill-first and expects the same, independently known, result from both.
func TestSelectorPickMatchesFillFirst(t *testing.T) {
	// Half a second past a whole second keeps the rounded reset time stable
	// between the two calls.
	future := time.Now().Add(42*time.Second + 500*time.Millisecond)
	cooling := func(id string) *coreauth.Auth {
		return &coreauth.Auth{ID: id, Provider: "codex", ModelStates: map[string]*coreauth.ModelState{
			"gpt-5": {Unavailable: true, NextRetryAfter: future, Quota: coreauth.QuotaState{Exceeded: true, NextRecoverAt: future}},
		}}
	}
	wsCtx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())

	cases := []struct {
		name     string
		ctx      context.Context
		auths    []*coreauth.Auth
		wantID   string
		wantErr  string // substring of the error text when no credential is picked
		errorTag string // error kind: "cooldown" or an auth error code
	}{
		{name: "highest priority tier wins", auths: []*coreauth.Auth{prio("c", "1"), prio("a", "0"), prio("b", "1")}, wantID: "b"},
		{name: "ID breaks ties", auths: []*coreauth.Auth{{ID: "z"}, {ID: "m"}, {ID: "a"}}, wantID: "a"},
		{name: "cooling credential skipped", auths: []*coreauth.Auth{cooling("a"), {ID: "b"}}, wantID: "b"},
		{name: "all cooling", auths: []*coreauth.Auth{cooling("a"), cooling("b")},
			wantErr: "All credentials for model gpt-5 are cooling down via provider codex", errorTag: "cooldown"},
		{name: "disabled credential skipped", auths: []*coreauth.Auth{{ID: "a", Disabled: true}, {ID: "b"}}, wantID: "b"},
		{name: "all disabled", auths: []*coreauth.Auth{{ID: "a", Disabled: true}, {ID: "b", Status: coreauth.StatusDisabled}},
			wantErr: "no auth available", errorTag: "auth_unavailable"},
		{name: "credential quota skipped", auths: []*coreauth.Auth{
			{ID: "a", Quota: coreauth.QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: future}}, {ID: "b"}}, wantID: "b"},
		{name: "model disabled on one credential", auths: []*coreauth.Auth{
			{ID: "a", ModelStates: map[string]*coreauth.ModelState{"gpt-5": {Status: coreauth.StatusDisabled}}}, {ID: "b"}}, wantID: "b"},
		{name: "codex websocket prefers websocket credential", ctx: wsCtx, auths: []*coreauth.Auth{
			{ID: "a"}, {ID: "b", Attributes: map[string]string{"websockets": "true"}}}, wantID: "b"},
		{name: "no websocket request keeps first credential", auths: []*coreauth.Auth{
			{ID: "a"}, {ID: "b", Attributes: map[string]string{"websockets": "true"}}}, wantID: "a"},
		{name: "no candidates", auths: nil, wantErr: "no auth candidates", errorTag: "auth_not_found"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := tc.ctx
			if ctx == nil {
				ctx = context.Background()
			}
			ak, akErr := akrouter.NewSelector().Pick(ctx, "codex", "gpt-5", cliproxyexecutor.Options{}, tc.auths)
			ff, ffErr := (&coreauth.FillFirstSelector{}).Pick(ctx, "codex", "gpt-5", cliproxyexecutor.Options{}, tc.auths)

			if tc.wantErr == "" {
				if akErr != nil || ffErr != nil {
					t.Fatalf("errors: ak-router %v, fill-first %v; want none", akErr, ffErr)
				}
				if ak == nil || ak.ID != tc.wantID {
					t.Fatalf("ak-router picked %v, want %q", idOf(ak), tc.wantID)
				}
				if ff == nil || ff.ID != tc.wantID {
					t.Fatalf("fill-first picked %v, want %q", idOf(ff), tc.wantID)
				}
				return
			}
			if ak != nil || ff != nil {
				t.Fatalf("picked ak-router %v, fill-first %v; want none", idOf(ak), idOf(ff))
			}
			for name, err := range map[string]error{"ak-router": akErr, "fill-first": ffErr} {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("%s error = %v, want text %q", name, err, tc.wantErr)
				}
				if got := errorKind(err); got != tc.errorTag {
					t.Fatalf("%s error kind = %q, want %q", name, got, tc.errorTag)
				}
			}
			if akErr.Error() != ffErr.Error() {
				t.Fatalf("error text differs: ak-router %q, fill-first %q", akErr, ffErr)
			}
		})
	}
}

func idOf(a *coreauth.Auth) any {
	if a == nil {
		return nil
	}
	return a.ID
}

func errorKind(err error) string {
	var cooldown interface{ IsModelCooldown() bool }
	if errors.As(err, &cooldown) && cooldown.IsModelCooldown() {
		return "cooldown"
	}
	var authErr *coreauth.Error
	if errors.As(err, &authErr) {
		return authErr.Code
	}
	return ""
}
