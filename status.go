package main

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// formatStatus renders one line per live session with its active leases.
// Files in one directory collapse to "dir/ (n)" unless verbose is set.
func formatStatus(st *State, self string, repo *Repo, verbose bool) string {
	var b strings.Builder
	if len(st.Sessions) == 0 {
		fmt.Fprintf(&b, "no live sessions in %s\n", repo.Top)
		return b.String()
	}
	for _, key := range st.sortedKeys() {
		s := st.Sessions[key]
		marker := " "
		if key == self {
			marker = "*"
		}
		note := "(no note)"
		if s.Note != "" {
			note = fmt.Sprintf("%q", s.Note)
		}
		fmt.Fprintf(&b, "%s %s  %s  touched %s ago\n", marker, shortID(s.SessionID), note, ago(s.Touched))
		if claims := summariseClaims(s, verbose); len(claims) > 0 {
			fmt.Fprintf(&b, "    %s\n", strings.Join(claims, "  "))
		}
	}
	for _, rel := range sortedWaiterKeys(st) {
		var ids []string
		for _, w := range st.Waiters[rel] {
			ids = append(ids, shortID(w.SessionID))
		}
		fmt.Fprintf(&b, "  waiting on %s: %s\n", rel, strings.Join(ids, ", "))
	}
	return b.String()
}

func summariseClaims(s *Session, verbose bool) []string {
	now := time.Now()
	claims := sortedClaims(s)
	if verbose {
		var out []string
		for _, c := range claims {
			out = append(out, describeClaim(c, s.Claims[c], now))
		}
		return out
	}
	perDir := map[string][]string{}
	var out []string
	for _, c := range claims {
		if strings.HasSuffix(c, "/") {
			out = append(out, describeClaim(c+"*", s.Claims[c], now))
			continue
		}
		perDir[dirOf(c)] = append(perDir[dirOf(c)], c)
	}
	dirs := make([]string, 0, len(perDir))
	for d := range perDir {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	for _, d := range dirs {
		files := perDir[d]
		if len(files) == 1 {
			out = append(out, describeClaim(files[0], s.Claims[files[0]], now))
			continue
		}
		out = append(out, fmt.Sprintf("%s (%d)", d, len(files)))
	}
	return out
}

func describeClaim(label string, c Claim, now time.Time) string {
	left := c.Until.Sub(now).Round(time.Second)
	if left < 0 {
		left = 0
	}
	suffix := "w"
	if c.Kind == kindRead {
		suffix = "r"
	}
	return fmt.Sprintf("%s[%s %ds]", label, suffix, int(left.Seconds()))
}

func sortedWaiterKeys(st *State) []string {
	keys := make([]string, 0, len(st.Waiters))
	for k := range st.Waiters {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
