package cliproxy

// Tests for the akc fork hooks H-1 and H-2 in service_config.go (see akc/README.md).

import (
	"context"
	"testing"

	akrouter "github.com/router-for-me/CLIProxyAPI/v8/akc/ak-router"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestAKCRoutingStrategyAKRouter(t *testing.T) {
	cases := []struct {
		value        string
		wantStrategy string
		wantAKRouter bool
	}{
		{value: "ak-router", wantStrategy: "ak-router", wantAKRouter: true},
		{value: " AK-Router ", wantStrategy: "ak-router", wantAKRouter: true},
		{value: "akr", wantStrategy: "round-robin"},
		{value: "akrouter", wantStrategy: "round-robin"},
		{value: "fill-first", wantStrategy: "fill-first"},
	}
	for _, tc := range cases {
		t.Run(tc.value, func(t *testing.T) {
			state := normalizedRoutingRuntimeState(&internalconfig.Config{
				Routing: internalconfig.RoutingConfig{Strategy: tc.value},
			})
			if state.strategy != tc.wantStrategy {
				t.Fatalf("strategy = %q, want %q", state.strategy, tc.wantStrategy)
			}
			selector := newRoutingSelector(state)
			if _, isAK := selector.(*akrouter.Selector); isAK != tc.wantAKRouter {
				t.Fatalf("selector type = %T, want ak-router selector: %v", selector, tc.wantAKRouter)
			}
		})
	}
}

func TestAKCRoutingStrategyAKRouterWithSessionAffinity(t *testing.T) {
	state := normalizedRoutingRuntimeState(&internalconfig.Config{
		Routing: internalconfig.RoutingConfig{Strategy: "ak-router", SessionAffinity: true},
	})
	selector := newRoutingSelector(state)
	affinity, ok := selector.(*coreauth.SessionAffinitySelector)
	if !ok {
		t.Fatalf("selector type = %T, want *auth.SessionAffinitySelector", selector)
	}
	defer affinity.Stop()

	// Without a session the wrapper falls back to ak-router, which keeps the
	// first credential; a round-robin fallback would move on to "b".
	auths := []*coreauth.Auth{{ID: "b"}, {ID: "a"}}
	for i := 0; i < 2; i++ {
		got, err := affinity.Pick(context.Background(), "codex", "gpt-5", cliproxyexecutor.Options{}, auths)
		if err != nil {
			t.Fatalf("Pick #%d error = %v", i, err)
		}
		if got.ID != "a" {
			t.Fatalf("Pick #%d = %q, want %q", i, got.ID, "a")
		}
	}
}

func TestAKCHotReloadSwitchesToAKRouter(t *testing.T) {
	service := &Service{coreManager: coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)}
	apply := func(seq uint64, strategy string) {
		t.Helper()
		commit := configCommit{cfg: &internalconfig.Config{Routing: internalconfig.RoutingConfig{Strategy: strategy}}, sequence: seq}
		if !service.applyManagerConfig(context.Background(), commit) {
			t.Fatalf("applyManagerConfig(%q) failed", strategy)
		}
	}
	apply(1, "fill-first")
	apply(2, "ak-router")
	if _, ok := service.coreManager.Selector().(*akrouter.Selector); !ok {
		t.Fatalf("selector after reload = %T, want *akrouter.Selector", service.coreManager.Selector())
	}
}
