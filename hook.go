package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const pollInterval = 250 * time.Millisecond

type hookInput struct {
	SessionID string         `json:"session_id"`
	CWD       string         `json:"cwd"`
	ToolName  string         `json:"tool_name"`
	ToolInput map[string]any `json:"tool_input"`
}

type repoTargets struct {
	repo *Repo
	rels []string
}

func runHook(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: coord hook pre|post|start|end")
	}
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		return err
	}
	var in hookInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return fmt.Errorf("parse hook input: %w", err)
	}
	if in.SessionID == "" {
		return fmt.Errorf("hook input has no session_id")
	}
	switch args[0] {
	case "pre":
		return hookPre(in)
	case "post":
		return hookPost(in)
	case "start":
		return hookStart(in)
	case "end":
		return hookEnd(in)
	}
	return fmt.Errorf("unknown hook event %q", args[0])
}

// hookPre waits until every target is free of conflicting leases, then takes
// leases on them and lets the tool call through. Only destructive commands
// are ever denied outright.
func hookPre(in hookInput) error {
	targets, kind, forbidden := toolTargets(in)
	if forbidden != "" {
		return deny("coord: " + forbidden + ".")
	}
	pid := claudePID()
	for _, rt := range groupByRepo(targets) {
		if err := acquire(rt, in.SessionID, kind, pid); err != nil {
			return err
		}
	}
	return nil
}

func acquire(rt repoTargets, sid, kind string, pid int) error {
	deadline := time.Now().Add(maxWait())
	for {
		var blocked *conflict
		forced := !time.Now().Before(deadline)
		err := withState(rt.repo, func(st *State) error {
			now := time.Now()
			s := st.ensure(sid, pid, now)
			if !forced {
				for _, rel := range rt.rels {
					if c := st.blocker(rel, kind, sid); c != nil {
						blocked = c
						break
					}
					if !st.firstInLine(rel, sid) {
						blocked = &conflict{Rel: rel}
						break
					}
				}
			}
			if blocked != nil {
				for _, rel := range rt.rels {
					st.enqueue(rel, sid, now)
				}
				return nil
			}
			st.dequeue(sid)
			for _, rel := range rt.rels {
				s.grant(rel, kind, now)
			}
			return nil
		})
		if err != nil {
			return err
		}
		if blocked == nil {
			if forced {
				fmt.Fprintf(os.Stderr, "coord: waited %s, proceeding anyway\n", maxWait())
			}
			return nil
		}
		time.Sleep(pollInterval)
	}
}

func hookPost(in hookInput) error {
	targets, kind, _ := toolTargets(in)
	if len(targets) == 0 {
		return nil
	}
	now := time.Now()
	for _, rt := range groupByRepo(targets) {
		err := withState(rt.repo, func(st *State) error {
			s := st.ensure(in.SessionID, 0, now)
			for _, rel := range rt.rels {
				s.grant(rel, kind, now)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func hookStart(in hookInput) error {
	repo, ok := FindRepo(in.CWD)
	if !ok {
		return nil
	}
	pid := claudePID()
	var report string
	err := withState(repo, func(st *State) error {
		st.ensure(in.SessionID, pid, time.Now())
		if othersActive(st, in.SessionID) {
			report = formatStatus(st, in.SessionID, repo, false)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if report != "" {
		fmt.Println("Other Claude sessions are working in this checkout (coord status):")
		fmt.Print(report)
	}
	return nil
}

func hookEnd(in hookInput) error {
	repo, ok := FindRepo(in.CWD)
	if !ok {
		return nil
	}
	return withState(repo, func(st *State) error {
		delete(st.Sessions, in.SessionID)
		st.dequeue(in.SessionID)
		return nil
	})
}

func othersActive(st *State, self string) bool {
	for key, s := range st.Sessions {
		if key != self && (len(s.Claims) > 0 || s.Note != "") {
			return true
		}
	}
	return false
}

func toolTargets(in hookInput) (paths []string, kind string, forbidden string) {
	kind = kindWrite
	switch in.ToolName {
	case "Read":
		kind = kindRead
		if p, _ := in.ToolInput["file_path"].(string); p != "" {
			paths = append(paths, resolve(in.CWD, p))
		}
	case "Edit", "Write", "MultiEdit":
		if p, _ := in.ToolInput["file_path"].(string); p != "" {
			paths = append(paths, resolve(in.CWD, p))
		}
	case "NotebookEdit":
		if p, _ := in.ToolInput["notebook_path"].(string); p != "" {
			paths = append(paths, resolve(in.CWD, p))
		}
	case "Bash":
		if cmd, _ := in.ToolInput["command"].(string); cmd != "" {
			paths, forbidden = bashWriteTargets(cmd, in.CWD)
		}
	}
	return paths, kind, forbidden
}

func groupByRepo(paths []string) []repoTargets {
	var groups []repoTargets
	byCommon := map[string]int{}
	for _, abs := range paths {
		repo, ok := FindRepo(abs)
		if !ok {
			continue
		}
		rel, ok := repo.Rel(abs)
		if !ok {
			continue
		}
		idx, seen := byCommon[repo.Common]
		if !seen {
			idx = len(groups)
			byCommon[repo.Common] = idx
			groups = append(groups, repoTargets{repo: repo})
		}
		groups[idx].rels = append(groups[idx].rels, rel)
	}
	return groups
}

func deny(reason string) error {
	out := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       "deny",
			"permissionDecisionReason": reason,
		},
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	return enc.Encode(out)
}

func ago(t time.Time) string {
	d := time.Since(t).Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func dirOf(rel string) string {
	if strings.HasSuffix(rel, "/") {
		return rel
	}
	return filepath.ToSlash(filepath.Dir(rel)) + "/"
}
