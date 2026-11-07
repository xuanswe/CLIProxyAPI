package akrouter_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	akrouter "github.com/router-for-me/CLIProxyAPI/v8/akc/ak-router"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

const parityModel = "gpt-5"

// parityMode is one routing configuration, built the way the service builds it.
type parityMode struct {
	name      string
	affinity  bool
	subagents bool
	rebindOn  string
}

func (m parityMode) selector(fallback coreauth.Selector) coreauth.Selector {
	if !m.affinity {
		return fallback
	}
	subagents := m.subagents
	return coreauth.NewSessionAffinitySelectorWithConfig(coreauth.SessionAffinityConfig{
		Fallback:         fallback,
		TTL:              time.Hour,
		SubagentAffinity: &subagents,
		RebindOn:         m.rebindOn,
	})
}

var parityModes = []parityMode{
	{name: "affinity-off"},
	{name: "affinity-on", affinity: true, subagents: true, rebindOn: coreauth.SessionAffinityRebindOnAny},
	{name: "affinity-on-subagents-off", affinity: true, subagents: false, rebindOn: coreauth.SessionAffinityRebindOnAny},
	{name: "affinity-rebind-on-quota", affinity: true, subagents: true, rebindOn: coreauth.SessionAffinityRebindOnQuota},
}

// parityStep changes credential state, then sends one request. want maps a
// mode name to the expected credential (or "error:<kind>"); "*" covers the rest.
type parityStep struct {
	label   string
	before  func(h *parityManager)
	session string
	parent  string
	want    map[string]string
}

// parityScript was derived by hand from fill-first's rules: the highest
// priority tier wins, then the lowest ID; session affinity keeps a bound
// credential while it is available.
var parityScript = []parityStep{
	{label: "first request", session: "s1", want: map[string]string{"*": "acc-a"}},
	{label: "second session", session: "s2", want: map[string]string{"*": "acc-a"}},
	{label: "acc-a hits quota", before: func(h *parityManager) { h.quota("acc-a") }, session: "s1",
		want: map[string]string{"*": "acc-b"}},
	{label: "acc-a recovers", before: func(h *parityManager) { h.recover("acc-a") }, session: "s1",
		want: map[string]string{"*": "acc-b", "affinity-off": "acc-a"}},
	{label: "untouched session", session: "s2", want: map[string]string{"*": "acc-a"}},
	{label: "subagent of s1", session: "c1", parent: "s1",
		want: map[string]string{"*": "acc-b", "affinity-off": "acc-a", "affinity-on-subagents-off": "acc-a"}},
	{label: "acc-a fails transiently", before: func(h *parityManager) { h.transient("acc-a") }, session: "s2",
		want: map[string]string{"*": "acc-b"}},
	{label: "acc-a recovers again", before: func(h *parityManager) { h.recover("acc-a") }, session: "s2",
		want: map[string]string{"*": "acc-b", "affinity-off": "acc-a", "affinity-rebind-on-quota": "acc-a"}},
	{label: "top tier on quota", before: func(h *parityManager) { h.quota("acc-a"); h.quota("acc-b") }, session: "s1",
		want: map[string]string{"*": "acc-c"}},
	{label: "every tier on quota", before: func(h *parityManager) { h.quota("acc-c") }, session: "s1",
		want: map[string]string{"*": "error:cooldown"}},
}

// TestManagerParityWithFillFirst runs the same requests and failures through a
// fill-first manager and an ak-router manager and expects the same picks.
func TestManagerParityWithFillFirst(t *testing.T) {
	coreauth.SetQuotaCooldownDisabled(false)
	for _, mode := range parityModes {
		for _, path := range []string{"execute", "select"} {
			t.Run(mode.name+"/"+path, func(t *testing.T) {
				prefix := fmt.Sprintf("parity-%s-%s-", mode.name, path)
				ff := newParityManager(t, prefix, mode.selector(&coreauth.FillFirstSelector{}))
				ak := newParityManager(t, prefix, mode.selector(akrouter.NewSelector()))
				for i, step := range parityScript {
					want, ok := step.want[mode.name]
					if !ok {
						want = step.want["*"]
					}
					gotFF := ff.step(path, step)
					gotAK := ak.step(path, step)
					if gotFF != want {
						t.Fatalf("step %d %q: fill-first got %s, want %s", i+1, step.label, gotFF, want)
					}
					if gotAK != gotFF {
						t.Fatalf("step %d %q: ak-router got %s, fill-first %s", i+1, step.label, gotAK, gotFF)
					}
				}
				if fmt.Sprint(ak.attempts) != fmt.Sprint(ff.attempts) {
					t.Fatalf("upstream attempts differ:\n ak-router  %v\n fill-first %v", ak.attempts, ff.attempts)
				}
			})
		}
	}
}

// parityManager is a real manager with three codex credentials and a stub
// executor that answers with the credential it was given.
type parityManager struct {
	t        *testing.T
	ctx      context.Context
	prefix   string
	manager  *coreauth.Manager
	attempts []string
}

func newParityManager(t *testing.T, prefix string, selector coreauth.Selector) *parityManager {
	t.Helper()
	if stopper, ok := selector.(interface{ Stop() }); ok {
		t.Cleanup(stopper.Stop)
	}
	h := &parityManager{t: t, ctx: context.Background(), prefix: prefix, manager: coreauth.NewManager(nil, selector, nil)}
	h.manager.RegisterExecutor(&parityExecutor{h: h})
	for _, c := range []struct{ id, priority string }{{"acc-c", "0"}, {"acc-b", "1"}, {"acc-a", "1"}} {
		id := prefix + c.id
		registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: parityModel}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
		auth := &coreauth.Auth{ID: id, Provider: "codex", Status: coreauth.StatusActive, Attributes: map[string]string{"priority": c.priority}}
		if _, err := h.manager.Register(h.ctx, auth); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}
	return h
}

func (h *parityManager) step(path string, step parityStep) string {
	h.t.Helper()
	if step.before != nil {
		step.before(h)
	}
	opts := cliproxyexecutor.Options{Metadata: map[string]any{}, Headers: http.Header{}}
	if step.session != "" {
		opts.Headers.Set("Session-Id", step.session)
	}
	if step.parent != "" {
		opts.Headers.Set("x-codex-parent-thread-id", step.parent)
	}
	switch path {
	case "select":
		auth, err := h.manager.SelectAuth(h.ctx, "codex", parityModel, opts)
		if err != nil {
			return "error:" + parityErrorKind(err)
		}
		return strings.TrimPrefix(auth.ID, h.prefix)
	default:
		resp, err := h.manager.Execute(h.ctx, []string{"codex"}, cliproxyexecutor.Request{Model: parityModel}, opts)
		if err != nil {
			return "error:" + parityErrorKind(err)
		}
		return strings.TrimPrefix(string(resp.Payload), h.prefix)
	}
}

func (h *parityManager) mark(id string, status int) {
	retryAfter := time.Hour
	h.manager.MarkResult(h.ctx, coreauth.Result{
		AuthID: h.prefix + id, Provider: "codex", Model: parityModel, RetryAfter: &retryAfter,
		Error: &coreauth.Error{HTTPStatus: status, Message: http.StatusText(status)},
	})
}

func (h *parityManager) quota(id string)     { h.mark(id, http.StatusTooManyRequests) }
func (h *parityManager) transient(id string) { h.mark(id, http.StatusServiceUnavailable) }

// recover moves every retry and recovery time of a credential into the past,
// as if its cooldown had run out, without waiting for the clock.
func (h *parityManager) recover(id string) {
	h.t.Helper()
	past := time.Now().Add(-time.Second)
	_, ok, err := h.manager.Modify(h.ctx, h.prefix+id, 0, func(a *coreauth.Auth) error {
		a.NextRetryAfter, a.Quota.NextRecoverAt = past, past
		for _, state := range a.ModelStates {
			if state != nil {
				state.NextRetryAfter, state.Quota.NextRecoverAt = past, past
			}
		}
		return nil
	})
	if err != nil || !ok {
		h.t.Fatalf("recover %s: ok=%v err=%v", id, ok, err)
	}
}

func parityErrorKind(err error) string {
	var cooldown interface{ IsModelCooldown() bool }
	if errors.As(err, &cooldown) && cooldown.IsModelCooldown() {
		return "cooldown"
	}
	var authErr *coreauth.Error
	if errors.As(err, &authErr) {
		return authErr.Code
	}
	return err.Error()
}

type parityExecutor struct{ h *parityManager }

func (e *parityExecutor) Identifier() string { return "codex" }

func (e *parityExecutor) Execute(_ context.Context, auth *coreauth.Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.h.attempts = append(e.h.attempts, strings.TrimPrefix(auth.ID, e.h.prefix))
	return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
}

func (e *parityExecutor) ExecuteStream(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, errors.New("parity executor: streaming not used")
}

func (e *parityExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *parityExecutor) CountTokens(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("parity executor: token counting not used")
}

func (e *parityExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("parity executor: raw HTTP not used")
}
