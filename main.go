package main

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

const usageText = `coord: file claims shared between Claude sessions on one checkout.

  coord status [-v]            live sessions, notes and claimed paths
  coord note "<intent>"        one line of what this session is doing
  coord note --clear
  coord claim <path>...        take a write lease ahead of editing (dirs end with /)
  coord release <path>...      drop leases early
  coord release --all          drop every lease this session holds
  coord release --session ID --all
  coord hook pre|post|start|end   Claude Code hook entry points (stdin JSON)

Leases are taken automatically by hooks on Read, Edit, Write and detected
shell writes, slide on every touch, and expire on their own (COORD_READ_LEASE
30s, COORD_WRITE_LEASE 60s). A tool call on a file another session holds
waits in line instead of failing (COORD_MAX_WAIT 2m).
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usageText)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "hook":
		if err = runHook(args); err != nil {
			fmt.Fprintln(os.Stderr, "coord hook:", err)
		}
		return
	case "status":
		err = runStatus(args)
	case "note":
		err = runNote(args)
	case "claim":
		err = runClaim(args)
	case "release":
		err = runRelease(args)
	case "help", "-h", "--help":
		fmt.Print(usageText)
	default:
		fmt.Fprint(os.Stderr, usageText)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "coord:", err)
		os.Exit(1)
	}
}

func repoFromCwd() (*Repo, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	repo, ok := FindRepo(cwd)
	if !ok {
		return nil, errors.New("not inside a git checkout")
	}
	return repo, nil
}

// selfKey finds this session's entry through the owning Claude process.
func selfKey(st *State, explicit string) (string, *Session, error) {
	if explicit != "" {
		if s, ok := st.Sessions[explicit]; ok {
			return explicit, s, nil
		}
		var matches []string
		for key := range st.Sessions {
			if strings.HasPrefix(key, explicit) {
				matches = append(matches, key)
			}
		}
		switch len(matches) {
		case 0:
			return "", nil, fmt.Errorf("no live session matches %q", explicit)
		case 1:
			return matches[0], st.Sessions[matches[0]], nil
		}
		sort.Strings(matches)
		return "", nil, fmt.Errorf("session %q is ambiguous: %s", explicit, strings.Join(matches, ", "))
	}
	pid := claudePID()
	if pid == 0 {
		return "", nil, errors.New("not running inside a Claude session; pass --session ID")
	}
	if key, s := st.byPID(pid); s != nil {
		return key, s, nil
	}
	key := fmt.Sprintf("pid:%d", pid)
	return key, st.ensure(key, pid, time.Now()), nil
}

func runStatus(args []string) error {
	verbose := len(args) > 0 && args[0] == "-v"
	repo, err := repoFromCwd()
	if err != nil {
		return err
	}
	return withState(repo, func(st *State) error {
		self := ""
		if pid := claudePID(); pid > 0 {
			self, _ = st.byPID(pid)
		}
		fmt.Print(formatStatus(st, self, repo, verbose))
		return nil
	})
}

func runNote(args []string) error {
	explicit, rest := takeSession(args)
	repo, err := repoFromCwd()
	if err != nil {
		return err
	}
	return withState(repo, func(st *State) error {
		_, s, err := selfKey(st, explicit)
		if err != nil {
			return err
		}
		if len(rest) > 0 && rest[0] == "--clear" {
			s.Note = ""
			return nil
		}
		if len(rest) == 0 {
			return errors.New("usage: coord note \"<intent>\"")
		}
		s.Note = strings.Join(rest, " ")
		s.Touched = time.Now()
		return nil
	})
}

func runClaim(args []string) error {
	explicit, paths := takeSession(args)
	if len(paths) == 0 {
		return errors.New("usage: coord claim <path>...")
	}
	cwd, _ := os.Getwd()
	var abs []string
	for _, p := range paths {
		abs = append(abs, resolve(cwd, p))
	}
	for _, rt := range groupByRepo(abs) {
		err := withState(rt.repo, func(st *State) error {
			key, s, err := selfKey(st, explicit)
			if err != nil {
				return err
			}
			now := time.Now()
			for _, rel := range rt.rels {
				if c := st.blocker(rel, kindWrite, key); c != nil {
					return fmt.Errorf("%s is held by session %s for another %s", rel, shortID(c.Holder.SessionID), c.Until.Sub(now).Round(time.Second))
				}
			}
			for _, rel := range rt.rels {
				s.Claims[rel] = Claim{Kind: kindWrite, Until: now.Add(leaseFor(kindWrite))}
			}
			s.Touched = now
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func runRelease(args []string) error {
	explicit, rest := takeSession(args)
	all := len(rest) > 0 && rest[0] == "--all"
	if !all && len(rest) == 0 {
		return errors.New("usage: coord release <path>... | --all")
	}
	repo, err := repoFromCwd()
	if err != nil {
		return err
	}
	cwd, _ := os.Getwd()
	return withState(repo, func(st *State) error {
		key, s, err := selfKey(st, explicit)
		if err != nil {
			return err
		}
		if all {
			s.Claims = map[string]Claim{}
			if strings.HasPrefix(key, "pid:") {
				delete(st.Sessions, key)
			}
			return nil
		}
		for _, p := range rest {
			rel, ok := repo.Rel(resolve(cwd, p))
			if ok {
				delete(s.Claims, rel)
			}
		}
		return nil
	})
}

func takeSession(args []string) (string, []string) {
	var rest []string
	explicit := ""
	for i := 0; i < len(args); i++ {
		if args[i] == "--session" && i+1 < len(args) {
			explicit = args[i+1]
			i++
			continue
		}
		rest = append(rest, args[i])
	}
	return explicit, rest
}
