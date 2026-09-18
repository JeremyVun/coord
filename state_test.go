package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCovers(t *testing.T) {
	cases := []struct {
		claim, rel string
		want       bool
	}{
		{"pkg/a.go", "pkg/a.go", true},
		{"pkg/a.go", "pkg/b.go", false},
		{"pkg/", "pkg/a.go", true},
		{"pkg/", "pkgx/a.go", false},
		{"pkg/a.go", "pkg/", true},
		{"pkg/sub/", "pkg/", true},
		{rootKey, "pkg/a.go", true},
		{"pkg/a.go", rootKey, true},
		{rootKey, rootKey, true},
	}
	for _, c := range cases {
		if got := covers(c.claim, c.rel); got != c.want {
			t.Errorf("covers(%q,%q)=%v want %v", c.claim, c.rel, got, c.want)
		}
	}
}

func TestRelMapsCheckoutRootToRootKey(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".git"), 0o755)
	repo, ok := FindRepo(root)
	if !ok {
		t.Fatal("repo not found")
	}
	if rel, ok := repo.Rel(root); !ok || rel != rootKey {
		t.Fatalf("Rel(root) = %q, %v", rel, ok)
	}
	if _, ok := repo.Rel(filepath.Dir(root)); ok {
		t.Fatal("parent of checkout should not map")
	}
}

func TestSelfKeyPrefixMustBeUnambiguous(t *testing.T) {
	now := time.Now()
	st := &State{Sessions: map[string]*Session{}}
	st.ensure("abc1", 0, now)
	st.ensure("abc12", 0, now)
	if _, _, err := selfKey(st, "abc"); err == nil {
		t.Fatal("ambiguous prefix should error")
	}
	if key, _, err := selfKey(st, "abc1"); err != nil || key != "abc1" {
		t.Fatalf("exact match should win: %q %v", key, err)
	}
	if key, _, err := selfKey(st, "abc12"); err != nil || key != "abc12" {
		t.Fatalf("unique prefix should resolve: %q %v", key, err)
	}
	if _, _, err := selfKey(st, "zzz"); err == nil {
		t.Fatal("no match should error")
	}
}

func TestPruneDropsDeadProcesses(t *testing.T) {
	now := time.Now()
	st := &State{Sessions: map[string]*Session{
		"live":  {PID: os.Getpid(), Touched: now, Claims: map[string]Claim{}},
		"dead":  {PID: 2147483000, Touched: now, Claims: map[string]Claim{}},
		"nopid": {PID: 0, Touched: now.Add(-13 * time.Hour), Claims: map[string]Claim{}},
		"fresh": {PID: 0, Touched: now, Claims: map[string]Claim{}},
	}}
	st.prune(now)
	for _, key := range []string{"live", "fresh"} {
		if _, ok := st.Sessions[key]; !ok {
			t.Errorf("%s should survive prune", key)
		}
	}
	for _, key := range []string{"dead", "nopid"} {
		if _, ok := st.Sessions[key]; ok {
			t.Errorf("%s should be pruned", key)
		}
	}
}

func TestEnsureAdoptsPIDPlaceholder(t *testing.T) {
	now := time.Now()
	st := &State{Sessions: map[string]*Session{}}
	placeholder := st.ensure("pid:42", 42, now)
	placeholder.Note = "wire codec"
	s := st.ensure("sess-1", 42, now)
	if s.Note != "wire codec" || len(st.Sessions) != 1 {
		t.Fatalf("placeholder not adopted: %+v", st.Sessions)
	}
}

func TestEnsureReplacesSessionSharingPositivePID(t *testing.T) {
	now := time.Now()
	st := &State{Sessions: map[string]*Session{}, Waiters: map[string][]Waiter{}}
	old := st.ensure("old-session", 42, now)
	old.Note = "old work"
	old.Claims["pkg/wire/codec.go"] = Claim{Kind: kindWrite, Until: now.Add(time.Minute)}
	st.enqueue("pkg/wire/codec.go", "old-session", now)

	replacement := st.ensure("new-session", 42, now.Add(time.Second))
	if len(st.Sessions) != 1 || st.Sessions["new-session"] != replacement {
		t.Fatalf("positive PID was not made unique: %+v", st.Sessions)
	}
	if replacement.Note != "" || len(replacement.Claims) != 0 {
		t.Fatalf("old session state leaked into replacement: %+v", replacement)
	}
	if len(st.Waiters) != 0 {
		t.Fatalf("old session waiters survived replacement: %+v", st.Waiters)
	}
}

func TestEnsureKnownPIDCorrectsExistingUnknownSession(t *testing.T) {
	now := time.Now()
	st := &State{Sessions: map[string]*Session{}}
	st.ensure("old-session", 42, now)
	current := st.ensure("current-session", 0, now)

	got := st.ensure("current-session", 42, now.Add(time.Second))
	if got != current || got.PID != 42 || len(st.Sessions) != 1 {
		t.Fatalf("unknown session was not corrected: %+v", st.Sessions)
	}
	if _, ok := st.Sessions["old-session"]; ok {
		t.Fatal("old row sharing the corrected PID survived")
	}
}

func TestEnsureAllowsMultipleUnknownPIDRows(t *testing.T) {
	now := time.Now()
	st := &State{Sessions: map[string]*Session{}}
	st.ensure("session-a", 0, now)
	st.ensure("session-b", 0, now)
	if len(st.Sessions) != 2 {
		t.Fatalf("PID 0 rows should not be deduplicated: %+v", st.Sessions)
	}
}

func TestByPIDPrefersMostRecentlyTouchedSession(t *testing.T) {
	now := time.Now()
	st := &State{Sessions: map[string]*Session{
		"older": {PID: 42, Touched: now},
		"newer": {PID: 42, Touched: now.Add(time.Second)},
	}}
	key, _ := st.byPID(42)
	if key != "newer" {
		t.Fatalf("byPID returned %q, want most recently touched session", key)
	}
}

func TestFindRepoWorktreeSharesCommonDir(t *testing.T) {
	root := t.TempDir()
	main := filepath.Join(root, "main")
	os.MkdirAll(filepath.Join(main, ".git", "worktrees", "wt"), 0o755)
	os.WriteFile(filepath.Join(main, ".git", "worktrees", "wt", "commondir"), []byte("../..\n"), 0o644)
	wt := filepath.Join(root, "wt")
	os.MkdirAll(filepath.Join(wt, "pkg"), 0o755)
	os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+filepath.Join(main, ".git", "worktrees", "wt")+"\n"), 0o644)

	a, ok := FindRepo(filepath.Join(main, "pkg", "x.go"))
	if !ok {
		t.Fatal("main repo not found")
	}
	b, ok := FindRepo(filepath.Join(wt, "pkg", "x.go"))
	if !ok {
		t.Fatal("worktree repo not found")
	}
	if a.Common != b.Common {
		t.Fatalf("common dirs differ: %s vs %s", a.Common, b.Common)
	}
	if rel, _ := b.Rel(filepath.Join(wt, "pkg")); rel != "pkg/" {
		t.Fatalf("dir rel = %q", rel)
	}
}
