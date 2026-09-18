package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func tempRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".git"), 0o755)
	os.MkdirAll(filepath.Join(root, "pkg", "wire"), 0o755)
	os.WriteFile(filepath.Join(root, "pkg", "wire", "codec.go"), nil, 0o644)
	return root
}

func captureStdout(t *testing.T, fn func() error) string {
	t.Helper()
	r, w, _ := os.Pipe()
	orig := os.Stdout
	os.Stdout = w
	err := fn()
	w.Close()
	os.Stdout = orig
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return b.String()
}

func decision(t *testing.T, out string) string {
	t.Helper()
	if strings.TrimSpace(out) == "" {
		return "allow"
	}
	var parsed struct {
		Out struct {
			Decision string `json:"permissionDecision"`
			Reason   string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("bad hook output %q: %v", out, err)
	}
	return parsed.Out.Decision + ": " + parsed.Out.Reason
}

func toolCall(root, sid, tool, file string) hookInput {
	return hookInput{SessionID: sid, CWD: root, ToolName: tool, ToolInput: map[string]any{"file_path": filepath.Join(root, file)}}
}

// timedPre runs the pre-hook and reports how long it blocked.
func timedPre(t *testing.T, in hookInput) (time.Duration, string) {
	t.Helper()
	start := time.Now()
	out := captureStdout(t, func() error { return hookPre(in) })
	return time.Since(start), decision(t, out)
}

func setLeases(t *testing.T, read, write, wait string) {
	t.Helper()
	t.Setenv("COORD_READ_LEASE", read)
	t.Setenv("COORD_WRITE_LEASE", write)
	t.Setenv("COORD_MAX_WAIT", wait)
}

func TestWriterWaitsForOtherWriterThenProceeds(t *testing.T) {
	setLeases(t, "500ms", "1s", "10s")
	root := tempRepo(t)
	if d, dec := timedPre(t, toolCall(root, "A", "Edit", "pkg/wire/codec.go")); d > 300*time.Millisecond || dec != "allow" {
		t.Fatalf("free file should be immediate, took %s (%s)", d, dec)
	}
	if d, _ := timedPre(t, toolCall(root, "A", "Edit", "pkg/wire/codec.go")); d > 300*time.Millisecond {
		t.Fatalf("own lease should not block, took %s", d)
	}
	d, dec := timedPre(t, toolCall(root, "B", "Edit", "pkg/wire/codec.go"))
	if dec != "allow" || d < 600*time.Millisecond || d > 3*time.Second {
		t.Fatalf("B should wait for A's lease to expire then proceed, took %s (%s)", d, dec)
	}
	if d, _ := timedPre(t, toolCall(root, "A", "Edit", "pkg/wire/codec.go")); d < 600*time.Millisecond {
		t.Fatalf("A should now wait on B's fresh lease, took %s", d)
	}
}

func TestReadersShareButWaitForWriters(t *testing.T) {
	setLeases(t, "1s", "1s", "10s")
	root := tempRepo(t)
	timedPre(t, toolCall(root, "A", "Read", "pkg/wire/codec.go"))
	if d, _ := timedPre(t, toolCall(root, "B", "Read", "pkg/wire/codec.go")); d > 300*time.Millisecond {
		t.Fatalf("two readers should not block, took %s", d)
	}
	if d, _ := timedPre(t, toolCall(root, "C", "Edit", "pkg/wire/codec.go")); d < 600*time.Millisecond {
		t.Fatalf("writer should wait for readers, took %s", d)
	}
	if d, _ := timedPre(t, toolCall(root, "A", "Read", "pkg/wire/codec.go")); d < 600*time.Millisecond {
		t.Fatalf("reader should wait for C's write lease, took %s", d)
	}
}

func TestShellWriteAndDirectoryDeleteWaitOnHeldFile(t *testing.T) {
	setLeases(t, "1s", "1s", "10s")
	root := tempRepo(t)
	timedPre(t, toolCall(root, "A", "Edit", "pkg/wire/codec.go"))
	sed := hookInput{SessionID: "B", CWD: root, ToolName: "Bash", ToolInput: map[string]any{"command": "sed -i '' 's/x/y/' pkg/wire/codec.go"}}
	if d, _ := timedPre(t, sed); d < 600*time.Millisecond {
		t.Fatalf("shell write should wait, took %s", d)
	}
	timedPre(t, toolCall(root, "A", "Edit", "pkg/wire/codec.go"))
	rm := hookInput{SessionID: "B", CWD: root, ToolName: "Bash", ToolInput: map[string]any{"command": "rm -rf " + filepath.Join(root, "pkg")}}
	if d, _ := timedPre(t, rm); d < 600*time.Millisecond {
		t.Fatalf("recursive delete over held file should wait, took %s", d)
	}
}

func TestCheckoutRootWriteWaitsOnAnyHeldFile(t *testing.T) {
	setLeases(t, "1s", "1s", "10s")
	root := tempRepo(t)
	timedPre(t, toolCall(root, "A", "Edit", "pkg/wire/codec.go"))
	restore := hookInput{SessionID: "B", CWD: root, ToolName: "Bash", ToolInput: map[string]any{"command": "git restore ."}}
	if d, dec := timedPre(t, restore); dec != "allow" || d < 600*time.Millisecond {
		t.Fatalf("whole-tree restore should wait for held file, took %s (%s)", d, dec)
	}
	if d, _ := timedPre(t, toolCall(root, "A", "Edit", "pkg/wire/codec.go")); d < 600*time.Millisecond {
		t.Fatalf("edit should wait for B's checkout-root lease, took %s", d)
	}
}

func TestReadKeepsOwnWriteLease(t *testing.T) {
	setLeases(t, "500ms", "2s", "10s")
	root := tempRepo(t)
	timedPre(t, toolCall(root, "A", "Edit", "pkg/wire/codec.go"))
	timedPre(t, toolCall(root, "A", "Read", "pkg/wire/codec.go"))
	if d, _ := timedPre(t, toolCall(root, "B", "Edit", "pkg/wire/codec.go")); d < 1200*time.Millisecond {
		t.Fatalf("A's read should not have shortened its write lease, B waited only %s", d)
	}
}

func TestSessionEndFreesImmediately(t *testing.T) {
	setLeases(t, "1m", "1m", "10s")
	root := tempRepo(t)
	timedPre(t, toolCall(root, "A", "Edit", "pkg/wire/codec.go"))
	if err := hookEnd(hookInput{SessionID: "A", CWD: root}); err != nil {
		t.Fatal(err)
	}
	if d, _ := timedPre(t, toolCall(root, "B", "Edit", "pkg/wire/codec.go")); d > 300*time.Millisecond {
		t.Fatalf("ended session should hold nothing, took %s", d)
	}
}

func TestMaxWaitProceedsAnyway(t *testing.T) {
	setLeases(t, "1m", "1m", "700ms")
	root := tempRepo(t)
	timedPre(t, toolCall(root, "A", "Edit", "pkg/wire/codec.go"))
	d, dec := timedPre(t, toolCall(root, "B", "Edit", "pkg/wire/codec.go"))
	if dec != "allow" || d < 600*time.Millisecond || d > 3*time.Second {
		t.Fatalf("B should give up waiting at the cap and proceed, took %s (%s)", d, dec)
	}
}

func TestWaitersAreServedInOrder(t *testing.T) {
	setLeases(t, "1m", "1m", "10s")
	root := tempRepo(t)
	repo, _ := FindRepo(root)
	withState(repo, func(st *State) error {
		st.ensure("A", 0, time.Now())
		st.ensure("B", 0, time.Now())
		st.ensure("C", 0, time.Now())
		st.enqueue("pkg/wire/codec.go", "B", time.Now().Add(-time.Second))
		return nil
	})
	if d, _ := timedPre(t, toolCall(root, "C", "Edit", "pkg/wire/codec.go")); d < 200*time.Millisecond {
		t.Fatalf("C should wait behind B even though the file is free, took %s", d)
	}
}

func TestStartReportsOnlyWhenOthersActive(t *testing.T) {
	setLeases(t, "1m", "1m", "10s")
	root := tempRepo(t)
	out := captureStdout(t, func() error { return hookStart(hookInput{SessionID: "A", CWD: root}) })
	if out != "" {
		t.Fatalf("lone session should print nothing, got %q", out)
	}
	repo, _ := FindRepo(root)
	withState(repo, func(st *State) error {
		s := st.ensure("A", 0, time.Now())
		s.Note = "wire codec"
		until := time.Now().Add(time.Minute)
		s.Claims["pkg/wire/codec.go"] = Claim{Kind: kindWrite, Until: until}
		s.Claims["pkg/wire/frame.go"] = Claim{Kind: kindWrite, Until: until}
		return nil
	})
	out = captureStdout(t, func() error { return hookStart(hookInput{SessionID: "B", CWD: root}) })
	if !strings.Contains(out, `"wire codec"`) || !strings.Contains(out, "pkg/wire/ (2)") {
		t.Fatalf("expected collapsed status, got %q", out)
	}
}

func TestForbiddenCommandsDenied(t *testing.T) {
	root := tempRepo(t)
	for _, cmd := range []string{"git update-ref refs/heads/main HEAD~1", "rm -rf build", "rm -rf " + root} {
		in := hookInput{SessionID: "A", CWD: root, ToolName: "Bash", ToolInput: map[string]any{"command": cmd}}
		if _, d := timedPre(t, in); !strings.HasPrefix(d, "deny:") {
			t.Fatalf("%q should be denied, got %s", cmd, d)
		}
	}
}
