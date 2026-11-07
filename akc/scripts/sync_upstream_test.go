package scripts

// Tests for sync-upstream.sh. Each test builds local repositories: an
// upstream with a small Go module, a bare "fork" cloned from it (as GitHub
// does), and a work clone of the fork on main-fork with one fork change. A
// fake gh reports the fork's Actions setting. Nothing touches the network.

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const hookFile = `package toy

// Strategy maps a routing value to a strategy name.
func Strategy(value string) string {
	switch value {
	case "ff":
		return "fill-first"
	}
	return "round-robin"
}
`

// forkHookFile adds one case line, like an akc hook.
const forkHookFile = `package toy

// Strategy maps a routing value to a strategy name.
func Strategy(value string) string {
	switch value {
	case "ff":
		return "fill-first"
	case "ak": // akc hook
		return "ak"
	}
	return "round-robin"
}
`

// conflictingHookFile edits the same lines as the fork hook.
const conflictingHookFile = `package toy

// Strategy maps a routing value to a strategy name.
func Strategy(value string) string {
	switch value {
	case "ff":
		return "fill-first"
	case "wrr":
		return "weighted"
	}
	return "round-robin"
}
`

type fixture struct {
	t        *testing.T
	root     string
	upstream string // upstream work tree, used as the upstream URL
	fork     string // bare fork repository
	work     string // clone of the fork on main-fork
	bin      string // holds the fake gh
	env      []string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	for _, tool := range []string{"bash", "git", "go", "gofmt"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not found", tool)
		}
	}
	root := t.TempDir()
	f := &fixture{t: t, root: root, upstream: filepath.Join(root, "upstream"), fork: filepath.Join(root, "fork.git"),
		work: filepath.Join(root, "work"), bin: filepath.Join(root, "bin")}
	f.env = isolatedEnv(f.bin)

	f.write(f.bin, "gh", `#!/bin/sh
if [ "$*" = "api repos/example/fork/actions/permissions --jq .enabled" ]; then
	echo "${FAKE_GH_ACTIONS_ENABLED:-false}"
	exit 0
fi
echo "fake gh: unexpected arguments: $*" >&2
exit 1
`)
	if err := os.Chmod(filepath.Join(f.bin, "gh"), 0o755); err != nil {
		t.Fatal(err)
	}

	f.git(root, "init", "-q", "-b", "main", f.upstream)
	f.write(f.upstream, "go.mod", "module example.com/toy\n\ngo 1.21\n")
	f.write(f.upstream, "cmd/server/main.go", "package main\n\nfunc main() {}\n")
	f.write(f.upstream, "hook.go", hookFile)
	f.write(f.upstream, "hook_test.go", "package toy\n\nimport \"testing\"\n\nfunc TestStrategy(t *testing.T) {\n\tif Strategy(\"ff\") != \"fill-first\" {\n\t\tt.Fatal(\"ff\")\n\t}\n}\n")
	f.commit(f.upstream, "initial")
	f.git(f.upstream, "tag", "-a", "v1.0.0", "-m", "v1.0.0")
	f.git(f.upstream, "branch", "dev")

	f.git(root, "clone", "-q", "--bare", f.upstream, f.fork)
	f.git(root, "clone", "-q", f.fork, f.work)
	f.git(f.work, "remote", "add", "upstream", f.upstream)
	f.git(f.work, "switch", "-q", "-c", "main-fork", "v1.0.0")
	f.write(f.work, "akc/README.md", "fork notes\n")
	f.write(f.work, "hook.go", forkHookFile)
	f.commit(f.work, "fork change")
	f.git(f.work, "push", "-q", "origin", "main-fork")
	return f
}

func isolatedEnv(bin string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GIT_") || strings.HasPrefix(kv, "AKC_") || strings.HasPrefix(kv, "PATH=") ||
			strings.HasPrefix(kv, "GOFLAGS=") || strings.HasPrefix(kv, "GOTOOLCHAIN=") || strings.HasPrefix(kv, "GOWORK=") {
			continue
		}
		env = append(env, kv)
	}
	return append(env,
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
		"GOTOOLCHAIN=local", "GOWORK=off", "GOFLAGS=",
		"AKC_FORK_REPO=example/fork", "AKC_SYNC_BASE=v1.0.0")
}

func (f *fixture) write(dir, name, content string) {
	f.t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) git(dir string, args ...string) string {
	f.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = f.env
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func (f *fixture) commit(dir, message string) {
	f.t.Helper()
	f.git(dir, "add", "-A")
	f.git(dir, "commit", "-q", "-m", message)
}

// release commits files to upstream main and tags the commit.
func (f *fixture) release(tag string, files map[string]string) {
	f.t.Helper()
	for name, content := range files {
		f.write(f.upstream, name, content)
	}
	f.commit(f.upstream, "release "+tag)
	f.git(f.upstream, "tag", "-a", tag, "-m", tag)
}

// ref returns the object a ref points to in a repository, or "" if missing.
func (f *fixture) ref(dir, ref string) string {
	f.t.Helper()
	cmd := exec.Command("git", "rev-parse", "--verify", "-q", ref)
	cmd.Dir = dir
	cmd.Env = f.env
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (f *fixture) forkRefs() string {
	return f.git(f.fork, "for-each-ref", "--format=%(refname) %(objectname)")
}

func (f *fixture) sync(extraEnv []string, args ...string) (string, int) {
	f.t.Helper()
	script, err := filepath.Abs("sync-upstream.sh")
	if err != nil {
		f.t.Fatal(err)
	}
	cmd := exec.Command("bash", append([]string{script}, args...)...)
	cmd.Dir = f.work
	cmd.Env = append(append([]string{}, f.env...), extraEnv...)
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return string(out), 0
	case errors.As(err, &exitErr):
		return string(out), exitErr.ExitCode()
	default:
		f.t.Fatalf("run sync: %v\n%s", err, out)
		return "", -1
	}
}

func (f *fixture) mustSync(args ...string) string {
	f.t.Helper()
	out, code := f.sync(nil, args...)
	if code != 0 {
		f.t.Fatalf("sync %v exit %d\n%s", args, code, out)
	}
	if strings.Contains(out, "error:") {
		f.t.Fatalf("sync %v succeeded but reported an error\n%s", args, out)
	}
	return out
}

func TestSyncFirstRunMirrorsAndTagsBase(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(f.upstream, "dev.txt", "dev work\n")
	f.git(f.upstream, "switch", "-q", "dev")
	f.commit(f.upstream, "dev work")
	f.git(f.upstream, "switch", "-q", "-c", "feat/x")
	f.git(f.upstream, "switch", "-q", "main")
	f.release("v1.0.1", map[string]string{"extra.go": "package toy\n\nconst Extra = 1\n"})
	f.git(f.fork, "branch", "gone", "main") // exists only in the fork
	stage1 := f.ref(f.work, "HEAD")

	out := f.mustSync("--until", "v1.0.0")

	for _, ref := range []string{"refs/heads/main", "refs/heads/dev", "refs/heads/feat/x", "refs/tags/v1.0.0", "refs/tags/v1.0.1"} {
		if got, want := f.ref(f.fork, ref), f.ref(f.upstream, ref); got != want {
			t.Errorf("fork %s = %q, want upstream's %q", ref, got, want)
		}
	}
	if got := f.ref(f.fork, "refs/tags/v1.0.0-fork^{commit}"); got != stage1 {
		t.Errorf("v1.0.0-fork = %q, want main-fork head %q", got, stage1)
	}
	if got := f.ref(f.fork, "refs/tags/v1.0.1-fork"); got != "" {
		t.Errorf("v1.0.1-fork exists after --until v1.0.0")
	}
	if f.ref(f.fork, "refs/heads/gone") == "" {
		t.Errorf("fork-only branch was deleted")
	}
	if !strings.Contains(out, "gone") {
		t.Errorf("output does not report the branch missing upstream:\n%s", out)
	}
}

func TestSyncMergesOneReleasePerRunWithUntil(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.release("v1.0.1", map[string]string{"one.go": "package toy\n\nconst One = 1\n"})
	f.release("v1.0.2", map[string]string{"two.go": "package toy\n\nconst Two = 2\n"})
	f.mustSync("--until", "v1.0.0")

	f.mustSync("--until", "v1.0.1")

	tagged := f.ref(f.fork, "refs/tags/v1.0.1-fork^{commit}")
	if tagged == "" {
		t.Fatal("v1.0.1-fork missing")
	}
	if f.ref(f.fork, "refs/tags/v1.0.2-fork") != "" {
		t.Fatal("v1.0.2-fork exists after --until v1.0.1")
	}
	if got := f.ref(f.fork, "refs/heads/main-fork"); got != tagged {
		t.Errorf("fork main-fork = %q, want tagged commit %q", got, tagged)
	}
	if f.ref(f.work, tagged+"^2") != f.ref(f.upstream, "v1.0.1^{commit}") {
		t.Errorf("v1.0.1-fork is not a merge of upstream v1.0.1")
	}
	if f.ref(f.work, tagged+":akc/README.md") == "" || f.ref(f.work, tagged+":one.go") == "" {
		t.Errorf("v1.0.1-fork lacks the fork change or the release change")
	}
	if f.ref(f.work, tagged+":two.go") != "" {
		t.Errorf("v1.0.1-fork contains v1.0.2")
	}

	f.mustSync()
	if f.ref(f.fork, "refs/tags/v1.0.2-fork") == "" {
		t.Fatal("v1.0.2-fork missing after a run without --until")
	}
}

func TestSyncDryRunPushesNothing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.release("v1.0.1", map[string]string{"one.go": "package toy\n\nconst One = 1\n"})
	before, head := f.forkRefs(), f.ref(f.work, "HEAD")

	out := f.mustSync("--dry-run")

	if f.forkRefs() != before {
		t.Error("dry run changed the fork")
	}
	if f.ref(f.work, "HEAD") != head {
		t.Error("dry run moved main-fork")
	}
	for _, want := range []string{"v1.0.0", "v1.0.1"} {
		if !strings.Contains(out, want) {
			t.Errorf("plan does not mention %s:\n%s", want, out)
		}
	}
}

func TestSyncStopsOnConflictAndResumesAfterManualMerge(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.release("v1.0.1", map[string]string{"hook.go": conflictingHookFile})
	f.mustSync("--until", "v1.0.0")
	head, forkHead := f.ref(f.work, "HEAD"), f.ref(f.fork, "refs/heads/main-fork")

	out, code := f.sync(nil)

	if code != 2 {
		t.Fatalf("exit %d, want 2\n%s", code, out)
	}
	if !strings.Contains(out, "hook.go") {
		t.Errorf("output does not name the conflicting file:\n%s", out)
	}
	if f.ref(f.work, "HEAD") != head || f.git(f.work, "status", "--porcelain") != "" {
		t.Error("conflicting merge was not aborted cleanly")
	}
	if f.ref(f.fork, "refs/heads/main-fork") != forkHead || f.ref(f.fork, "refs/tags/v1.0.1-fork") != "" {
		t.Error("something was pushed for the conflicting release")
	}

	cmd := exec.Command("git", "merge", "-q", "--no-edit", "v1.0.1")
	cmd.Dir, cmd.Env = f.work, f.env
	_ = cmd.Run() // conflicts; resolved below
	f.write(f.work, "hook.go", strings.Replace(conflictingHookFile, "\t}\n\treturn", "\tcase \"ak\": // akc hook\n\t\treturn \"ak\"\n\t}\n\treturn", 1))
	f.commit(f.work, "Merge upstream release v1.0.1 into main-fork")

	f.mustSync()
	if got := f.ref(f.fork, "refs/tags/v1.0.1-fork^{commit}"); got != f.ref(f.work, "HEAD") {
		t.Errorf("v1.0.1-fork = %q, want the manual merge %q", got, f.ref(f.work, "HEAD"))
	}
}

func TestSyncStopsOnFailedCheck(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.release("v1.0.1", map[string]string{"broken_test.go": "package toy\n\nimport \"testing\"\n\nfunc TestBroken(t *testing.T) { t.Fatal(\"broken\") }\n"})
	f.mustSync("--until", "v1.0.0")
	forkHead := f.ref(f.fork, "refs/heads/main-fork")

	out, code := f.sync(nil)

	if code != 3 {
		t.Fatalf("exit %d, want 3\n%s", code, out)
	}
	if f.ref(f.fork, "refs/heads/main-fork") != forkHead || f.ref(f.fork, "refs/tags/v1.0.1-fork") != "" {
		t.Error("something was pushed after a failed check")
	}
	if f.ref(f.work, "refs/tags/v1.0.1-fork") != "" {
		t.Error("v1.0.1-fork was tagged locally after a failed check")
	}
}

func TestSyncStopsOnUnformattedForkCode(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(f.work, "akc/fork.go", "package toy\nconst  Fork = 1\n")
	f.commit(f.work, "unformatted fork code")
	before := f.forkRefs()

	out, code := f.sync(nil, "--until", "v1.0.0")

	if code != 3 {
		t.Fatalf("exit %d, want 3\n%s", code, out)
	}
	if !strings.Contains(out, "akc/fork.go") {
		t.Errorf("output does not name the unformatted file:\n%s", out)
	}
	if f.forkRefs() != before {
		t.Error("fork changed after unformatted fork code")
	}
}

func TestSyncStopsWhenUpstreamTagChanged(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		olderTag func(f *fixture) // leaves the old v1.0.0 in exactly one place
		where    string
	}{
		{name: "fork copy", olderTag: func(f *fixture) { f.git(f.work, "tag", "-d", "v1.0.0") }, where: "fork has"},
		{name: "local copy", olderTag: func(f *fixture) { f.git(f.fork, "tag", "-d", "v1.0.0") }, where: "local has"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.write(f.upstream, "moved.txt", "moved\n")
			f.commit(f.upstream, "retag target")
			f.git(f.upstream, "tag", "-f", "-a", "v1.0.0", "-m", "v1.0.0 moved")
			tc.olderTag(f)
			before := f.forkRefs()

			out, code := f.sync(nil)

			if code != 4 {
				t.Fatalf("exit %d, want 4\n%s", code, out)
			}
			if !strings.Contains(out, "upstream tag v1.0.0 changed: "+tc.where) {
				t.Errorf("output does not name the changed tag and copy:\n%s", out)
			}
			if f.forkRefs() != before {
				t.Error("fork changed after a changed upstream tag")
			}
		})
	}
}

func TestSyncPreconditions(t *testing.T) {
	t.Parallel()
	none := func(*fixture) []string { return nil }
	cases := []struct {
		name   string
		setup  func(f *fixture) []string
		args   []string
		reason string
	}{
		{name: "actions enabled", setup: func(*fixture) []string { return []string{"FAKE_GH_ACTIONS_ENABLED=true"} },
			reason: "Actions are enabled"},
		{name: "untracked file", setup: func(f *fixture) []string { f.write(f.work, "scratch.txt", "x\n"); return nil },
			reason: "not clean"},
		{name: "not on main-fork", setup: func(f *fixture) []string { f.git(f.work, "switch", "-q", "main"); return nil },
			reason: "check out main-fork"},
		{name: "bad --until", setup: none, args: []string{"--until", "latest"}, reason: "--until needs a release tag"},
		{name: "unknown --until release", setup: none, args: []string{"--until", "v9.9.9"}, reason: "not an upstream tag"},
		{name: "fork main-fork ahead", setup: func(f *fixture) []string {
			f.write(f.work, "local.txt", "x\n")
			f.commit(f.work, "pushed elsewhere")
			f.git(f.work, "push", "-q", "origin", "main-fork")
			f.git(f.work, "reset", "-q", "--hard", "HEAD~1")
			return nil
		}, reason: "commits missing locally"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.release("v1.0.1", map[string]string{"one.go": "package toy\n\nconst One = 1\n"})
			env := tc.setup(f)
			before := f.forkRefs()

			out, code := f.sync(env, tc.args...)

			if code != 1 {
				t.Fatalf("exit %d, want 1\n%s", code, out)
			}
			if !strings.Contains(out, tc.reason) {
				t.Errorf("output lacks %q:\n%s", tc.reason, out)
			}
			if f.forkRefs() != before {
				t.Error("fork changed after a failed precondition")
			}
		})
	}
}

func TestSyncAddsMissingUpstreamRemote(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.git(f.work, "remote", "remove", "upstream")

	out, code := f.sync([]string{"AKC_UPSTREAM_URL=" + f.upstream}, "--dry-run")

	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if got := f.git(f.work, "remote", "get-url", "upstream"); got != f.upstream {
		t.Errorf("upstream remote = %q, want %q", got, f.upstream)
	}
}
