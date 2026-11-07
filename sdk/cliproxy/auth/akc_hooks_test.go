package auth

// Tests for the akc fork hooks H-3 (conductor_selection.go) and H-4
// (scheduler.go); see akc/README.md. This package cannot import akrouter
// (import cycle), so a local type carries the marker method.

import (
	"context"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type akcFillFirstEquivalent struct{ FillFirstSelector }

func (*akcFillFirstEquivalent) FillFirstEquivalent() {}

type akcCustomSelector struct{}

func (akcCustomSelector) Pick(context.Context, string, string, cliproxyexecutor.Options, []*Auth) (*Auth, error) {
	return nil, nil
}

func TestAKCFillFirstEquivalentIsBuiltIn(t *testing.T) {
	if !isBuiltInSelector(&akcFillFirstEquivalent{}) {
		t.Fatal("selector with FillFirstEquivalent is not built-in")
	}
	if isBuiltInSelector(akcCustomSelector{}) {
		t.Fatal("custom selector without the marker is built-in")
	}
}

func TestAKCFillFirstEquivalentUsesFillFirstScheduling(t *testing.T) {
	if got := selectorStrategy(&akcFillFirstEquivalent{}); got != schedulerStrategyFillFirst {
		t.Fatalf("selectorStrategy = %v, want fill-first (%v)", got, schedulerStrategyFillFirst)
	}
	if got := selectorStrategy(akcCustomSelector{}); got != schedulerStrategyCustom {
		t.Fatalf("custom selectorStrategy = %v, want custom (%v)", got, schedulerStrategyCustom)
	}
}
