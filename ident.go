package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type proc struct {
	ppid int
	cmd  string
}

// claudePID walks up from this process to the Claude Code process that owns
// the session, so hooks and model-run commands agree on one identity.
func claudePID() int {
	procs := listProcs()
	pid := os.Getppid()
	for depth := 0; depth < 32 && pid > 1; depth++ {
		p, ok := procs[pid]
		if !ok {
			return 0
		}
		if isClaude(p.cmd) {
			return pid
		}
		pid = p.ppid
	}
	return 0
}

func listProcs() map[int]proc {
	out, err := exec.Command("ps", "-axwwo", "pid=,ppid=,command=").Output()
	procs := map[int]proc{}
	if err != nil {
		return procs
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, err1 := strconv.Atoi(fields[0])
		ppid, err2 := strconv.Atoi(fields[1])
		if err1 != nil || err2 != nil {
			continue
		}
		procs[pid] = proc{ppid: ppid, cmd: strings.Join(fields[2:], " ")}
	}
	return procs
}

func isClaude(cmd string) bool {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return false
	}
	base := filepath.Base(fields[0])
	if base == "claude" {
		return true
	}
	if strings.HasPrefix(base, "node") && len(fields) > 1 {
		return strings.Contains(fields[1], "claude")
	}
	return false
}
