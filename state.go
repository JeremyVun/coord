package main

import (
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	kindRead  = "read"
	kindWrite = "write"
	rootKey   = "./"
)

type Claim struct {
	Kind  string    `json:"kind"`
	Until time.Time `json:"until"`
}

type Waiter struct {
	SessionID string    `json:"session_id"`
	Since     time.Time `json:"since"`
}

type Session struct {
	SessionID string           `json:"session_id"`
	PID       int              `json:"pid"`
	Note      string           `json:"note,omitempty"`
	Started   time.Time        `json:"started"`
	Touched   time.Time        `json:"touched"`
	Claims    map[string]Claim `json:"claims"`
}

type State struct {
	Sessions map[string]*Session `json:"sessions"`
	Waiters  map[string][]Waiter `json:"waiters,omitempty"`
}

// withState runs fn under the repo lock with pruned state and persists the result.
func withState(repo *Repo, fn func(*State) error) error {
	lock, err := os.OpenFile(repo.LockPath(), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	st := &State{}
	if raw, err := os.ReadFile(repo.StatePath()); err == nil {
		if err := json.Unmarshal(raw, st); err != nil {
			st = &State{}
		}
	}
	if st.Sessions == nil {
		st.Sessions = map[string]*Session{}
	}
	if st.Waiters == nil {
		st.Waiters = map[string][]Waiter{}
	}
	st.prune(time.Now())
	if err := fn(st); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := repo.StatePath() + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, repo.StatePath())
}

// prune drops dead sessions, expired leases and waiters that gave up or died.
func (st *State) prune(now time.Time) {
	for key, s := range st.Sessions {
		if !sessionAlive(s, now) {
			delete(st.Sessions, key)
			continue
		}
		for rel, c := range s.Claims {
			if !now.Before(c.Until) {
				delete(s.Claims, rel)
			}
		}
	}
	for rel, ws := range st.Waiters {
		var live []Waiter
		for _, w := range ws {
			s, ok := st.Sessions[w.SessionID]
			if ok && sessionAlive(s, now) && now.Sub(w.Since) <= maxWait() {
				live = append(live, w)
			}
		}
		if len(live) == 0 {
			delete(st.Waiters, rel)
		} else {
			st.Waiters[rel] = live
		}
	}
}

const unknownPIDExpiry = 12 * time.Hour

func sessionAlive(s *Session, now time.Time) bool {
	if s.PID > 0 {
		return processAlive(s.PID)
	}
	return now.Sub(s.Touched) <= unknownPIDExpiry
}

func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// ensure returns the session for sid, adopting any entry that was created by
// PID alone before the session id was known.
func (st *State) ensure(sid string, pid int, now time.Time) *Session {
	if s, ok := st.Sessions[sid]; ok {
		if pid > 0 {
			st.removeOtherPIDRows(sid, pid)
			s.PID = pid
		}
		s.Touched = now
		return s
	}
	if pid > 0 {
		placeholderKey := "pid:" + strconv.Itoa(pid)
		if s, ok := st.Sessions[placeholderKey]; ok && placeholderKey != sid {
			delete(st.Sessions, placeholderKey)
			st.dequeue(placeholderKey)
			s.SessionID = sid
			s.Touched = now
			st.Sessions[sid] = s
			st.removeOtherPIDRows(sid, pid)
			return s
		}
		st.removeOtherPIDRows(sid, pid)
	}
	s := &Session{SessionID: sid, PID: pid, Started: now, Touched: now, Claims: map[string]Claim{}}
	st.Sessions[sid] = s
	return s
}

// removeOtherPIDRows enforces one session per known Claude process. Session
// ids can change within a process after commands such as /clear and /resume.
func (st *State) removeOtherPIDRows(keep string, pid int) {
	for key, s := range st.Sessions {
		if key != keep && s.PID == pid {
			delete(st.Sessions, key)
			st.dequeue(key)
		}
	}
}

// grant issues a fresh lease, never downgrading a write the session already holds.
func (s *Session) grant(rel, kind string, now time.Time) {
	if held, ok := s.Claims[rel]; ok && held.Kind == kindWrite {
		kind = kindWrite
	}
	s.Claims[rel] = Claim{Kind: kind, Until: now.Add(leaseFor(kind))}
}

func (st *State) byPID(pid int) (string, *Session) {
	var bestKey string
	var best *Session
	for key, s := range st.Sessions {
		if s.PID != pid {
			continue
		}
		if best == nil || s.Touched.After(best.Touched) || (s.Touched.Equal(best.Touched) && key < bestKey) {
			bestKey, best = key, s
		}
	}
	return bestKey, best
}

// covers reports whether a claim and a requested path overlap in either direction.
func covers(claim, rel string) bool {
	if claim == rel || claim == rootKey || rel == rootKey {
		return true
	}
	if strings.HasSuffix(claim, "/") && strings.HasPrefix(rel, claim) {
		return true
	}
	if strings.HasSuffix(rel, "/") && strings.HasPrefix(claim, rel) {
		return true
	}
	return false
}

type conflict struct {
	Rel    string
	Claim  string
	Kind   string
	Until  time.Time
	Holder *Session
}

// blocker returns the first lease held by another session that excludes the
// requested kind: writers exclude everyone, readers exclude only writers.
func (st *State) blocker(rel, kind, exclude string) *conflict {
	for _, key := range st.sortedKeys() {
		if key == exclude {
			continue
		}
		s := st.Sessions[key]
		for _, c := range sortedClaims(s) {
			held := s.Claims[c]
			if kind == kindRead && held.Kind == kindRead {
				continue
			}
			if covers(c, rel) {
				return &conflict{Rel: rel, Claim: c, Kind: held.Kind, Until: held.Until, Holder: s}
			}
		}
	}
	return nil
}

// firstInLine reports whether sid is the oldest live waiter for rel, or nobody waits.
func (st *State) firstInLine(rel, sid string) bool {
	ws := st.Waiters[rel]
	return len(ws) == 0 || ws[0].SessionID == sid
}

func (st *State) enqueue(rel, sid string, now time.Time) {
	for _, w := range st.Waiters[rel] {
		if w.SessionID == sid {
			return
		}
	}
	st.Waiters[rel] = append(st.Waiters[rel], Waiter{SessionID: sid, Since: now})
}

func (st *State) dequeue(sid string) {
	for rel, ws := range st.Waiters {
		var keep []Waiter
		for _, w := range ws {
			if w.SessionID != sid {
				keep = append(keep, w)
			}
		}
		if len(keep) == 0 {
			delete(st.Waiters, rel)
		} else {
			st.Waiters[rel] = keep
		}
	}
}

func (st *State) sortedKeys() []string {
	keys := make([]string, 0, len(st.Sessions))
	for k := range st.Sessions {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedClaims(s *Session) []string {
	claims := make([]string, 0, len(s.Claims))
	for c := range s.Claims {
		claims = append(claims, c)
	}
	sort.Strings(claims)
	return claims
}

func shortID(sid string) string {
	sid = strings.TrimPrefix(sid, "pid:")
	if len(sid) > 6 {
		return sid[:6]
	}
	return sid
}

func envDuration(name string, fallback time.Duration) time.Duration {
	if v := os.Getenv(name); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}

func leaseFor(kind string) time.Duration {
	if kind == kindRead {
		return envDuration("COORD_READ_LEASE", 30*time.Second)
	}
	return envDuration("COORD_WRITE_LEASE", 60*time.Second)
}

func maxWait() time.Duration { return envDuration("COORD_MAX_WAIT", 2*time.Minute) }
