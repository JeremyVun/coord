package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// bashWriteTargets extracts the files a shell command will write, move or
// delete, resolved against cwd. Detection is best effort: inline python or
// perl scripts that open files themselves are invisible to it.
func bashWriteTargets(cmd, cwd string) (targets []string, forbidden string) {
	segments := splitSegments(tokenize(stripHeredocs(cmd)))
	for _, seg := range segments {
		argv, redirects := splitRedirects(seg)
		for _, r := range redirects {
			targets = appendResolved(targets, cwd, r)
		}
		argv = stripPrefixWords(argv)
		if len(argv) == 0 {
			continue
		}
		base := filepath.Base(argv[0])
		args := argv[1:]
		switch base {
		case "cd":
			if nf := nonFlags(args); len(nf) > 0 {
				cwd = resolve(cwd, nf[0])
			} else {
				cwd, _ = os.UserHomeDir()
			}
		case "sed":
			if hasFlagPrefix(args, "-i") || hasFlag(args, "--in-place") {
				for _, p := range sedFiles(args) {
					targets = appendResolved(targets, cwd, p)
				}
			}
		case "tee", "touch", "truncate", "mkdir":
			for _, p := range nonFlags(args) {
				targets = appendResolved(targets, cwd, p)
			}
		case "mv":
			for _, p := range nonFlags(args) {
				targets = appendResolved(targets, cwd, p)
			}
		case "cp", "rsync", "ln":
			if nf := nonFlags(args); len(nf) > 0 {
				targets = appendResolved(targets, cwd, nf[len(nf)-1])
			}
		case "rm":
			nf := nonFlags(args)
			if hasRecursiveFlag(args) {
				for _, p := range nf {
					if !filepath.IsAbs(p) && !strings.HasPrefix(p, "~") {
						return targets, "recursive rm with a relative path (" + p + "); use an absolute path"
					}
					if containsCheckout(resolve(cwd, p), cwd) {
						return targets, "recursive rm of " + p + " would delete the checkout"
					}
				}
			}
			for _, p := range nf {
				targets = appendResolved(targets, cwd, p)
			}
		case "dd":
			for _, a := range args {
				if strings.HasPrefix(a, "of=") {
					targets = appendResolved(targets, cwd, strings.TrimPrefix(a, "of="))
				}
			}
		case "gofmt", "goimports":
			if hasFlag(args, "-w") {
				for _, p := range nonFlags(args) {
					targets = appendResolved(targets, cwd, p)
				}
			}
		case "prettier":
			if hasFlag(args, "--write") || hasFlag(args, "-w") {
				for _, p := range nonFlags(args) {
					targets = appendResolved(targets, cwd, p)
				}
			}
		case "eslint":
			if hasFlag(args, "--fix") {
				for _, p := range nonFlags(args) {
					targets = appendResolved(targets, cwd, p)
				}
			}
		case "git":
			paths, reason := gitTargets(args, cwd)
			if reason != "" {
				return targets, reason
			}
			for _, p := range paths {
				targets = appendResolved(targets, cwd, p)
			}
		}
	}
	return targets, ""
}

var gitGlobalWithValue = map[string]bool{"-C": true, "-c": true, "--git-dir": true, "--work-tree": true}

func gitTargets(args []string, cwd string) (paths []string, forbidden string) {
	i := 0
	for i < len(args) && strings.HasPrefix(args[i], "-") {
		if gitGlobalWithValue[args[i]] {
			i++
		}
		i++
	}
	if i >= len(args) {
		return nil, ""
	}
	sub, rest := args[i], args[i+1:]
	switch sub {
	case "update-ref":
		return nil, "git update-ref is forbidden on shared branches; use git commit or merge"
	case "checkout", "switch":
		if idx := indexOf(rest, "--"); idx >= 0 {
			return rest[idx+1:], ""
		}
		for _, p := range nonFlags(rest) {
			if _, err := os.Stat(resolve(cwd, p)); err == nil {
				paths = append(paths, p)
			}
		}
		return paths, ""
	case "restore", "rm", "mv", "add":
		return nonFlags(stripValued(rest, "-s", "--source")), ""
	case "stash":
		if len(rest) == 0 || !stashReadOnly[rest[0]] {
			return checkoutRoot(cwd), ""
		}
	case "reset":
		if hasFlag(rest, "--hard") || hasFlag(rest, "--merge") || hasFlag(rest, "--keep") {
			return checkoutRoot(cwd), ""
		}
	case "clean":
		force := hasShortFlag(rest, "f") || hasFlag(rest, "--force")
		dryRun := hasShortFlag(rest, "n") || hasFlag(rest, "--dry-run")
		if force && !dryRun {
			return checkoutRoot(cwd), ""
		}
	}
	return nil, ""
}

var stashReadOnly = map[string]bool{"list": true, "show": true}

func checkoutRoot(cwd string) []string {
	if repo, ok := FindRepo(cwd); ok {
		return []string{repo.Top}
	}
	return nil
}

// containsCheckout reports whether abs is the checkout around cwd or one of its ancestors.
func containsCheckout(abs, cwd string) bool {
	repo, ok := FindRepo(cwd)
	if !ok {
		return false
	}
	rel, err := filepath.Rel(abs, repo.Top)
	return err == nil && !strings.HasPrefix(rel, "..")
}

func stripValued(args []string, flags ...string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		if indexOf(flags, args[i]) >= 0 {
			i++
			continue
		}
		out = append(out, args[i])
	}
	return out
}

func sedFiles(args []string) []string {
	var files []string
	scriptGiven := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-e" || a == "-f" || a == "--expression" || a == "--file":
			scriptGiven = true
			i++
		case strings.HasPrefix(a, "-e") || strings.HasPrefix(a, "--expression="):
			scriptGiven = true
		case strings.HasPrefix(a, "-"):
		case !scriptGiven:
			scriptGiven = true
		default:
			files = append(files, a)
		}
	}
	return files
}

var prefixWords = map[string]bool{"sudo": true, "nohup": true, "time": true, "command": true, "exec": true, "builtin": true, "env": true}
var envAssign = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

func stripPrefixWords(argv []string) []string {
	for len(argv) > 0 && (prefixWords[filepath.Base(argv[0])] || envAssign.MatchString(argv[0])) {
		argv = argv[1:]
	}
	return argv
}

func nonFlags(args []string) []string {
	var out []string
	for _, a := range args {
		if a == "--" {
			continue
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			continue
		}
		out = append(out, a)
	}
	return out
}

func hasFlag(args []string, flag string) bool { return indexOf(args, flag) >= 0 }

func hasFlagPrefix(args []string, prefix string) bool {
	for _, a := range args {
		if strings.HasPrefix(a, prefix) && !strings.HasPrefix(a, "--") {
			return true
		}
	}
	return false
}

func hasRecursiveFlag(args []string) bool {
	return hasFlag(args, "--recursive") || hasShortFlag(args, "rR")
}

func hasShortFlag(args []string, letters string) bool {
	for _, a := range args {
		if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.ContainsAny(a, letters) {
			return true
		}
	}
	return false
}

func indexOf(list []string, want string) int {
	for i, s := range list {
		if s == want {
			return i
		}
	}
	return -1
}

func resolve(cwd, p string) string {
	if strings.HasPrefix(p, "~/") || p == "~" {
		home, _ := os.UserHomeDir()
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(cwd, p)
	}
	return filepath.Clean(p)
}

func appendResolved(targets []string, cwd, p string) []string {
	if p == "" || strings.HasPrefix(p, "/dev/") || strings.HasPrefix(p, "&") {
		return targets
	}
	if strings.ContainsAny(p, "*?[") {
		matches, _ := filepath.Glob(resolve(cwd, p))
		return append(targets, matches...)
	}
	return append(targets, resolve(cwd, p))
}

// --- tokenizer ---

type token struct {
	text string
	op   bool
}

var heredocRe = regexp.MustCompile(`<<-?\s*(['"]?)([A-Za-z_][A-Za-z0-9_]*)['"]?`)

func stripHeredocs(cmd string) string {
	for {
		loc := heredocRe.FindStringSubmatchIndex(cmd)
		if loc == nil {
			return cmd
		}
		delim := cmd[loc[4]:loc[5]]
		lineEnd := strings.Index(cmd[loc[1]:], "\n")
		if lineEnd < 0 {
			return cmd[:loc[0]] + cmd[loc[1]:]
		}
		bodyStart := loc[1] + lineEnd + 1
		rest := cmd[bodyStart:]
		bodyEnd := len(rest)
		for _, m := range regexp.MustCompile(`(?m)^\s*`+regexp.QuoteMeta(delim)+`\s*$`).FindAllStringIndex(rest, 1) {
			bodyEnd = m[1]
		}
		cmd = cmd[:loc[0]] + cmd[loc[1]:bodyStart] + rest[bodyEnd:]
	}
}

func tokenize(cmd string) []token {
	var toks []token
	var buf strings.Builder
	flush := func() {
		if buf.Len() > 0 {
			toks = append(toks, token{text: buf.String()})
			buf.Reset()
		}
	}
	pushOp := func(op string) {
		flush()
		toks = append(toks, token{text: op, op: true})
	}
	runes := []rune(cmd)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch {
		case c == '\\' && i+1 < len(runes):
			i++
			buf.WriteRune(runes[i])
		case c == '\'':
			for i++; i < len(runes) && runes[i] != '\''; i++ {
				buf.WriteRune(runes[i])
			}
		case c == '"':
			for i++; i < len(runes) && runes[i] != '"'; i++ {
				if runes[i] == '\\' && i+1 < len(runes) {
					i++
				}
				buf.WriteRune(runes[i])
			}
		case c == ' ' || c == '\t':
			flush()
		case c == '\n' || c == ';' || c == '(' || c == ')':
			pushOp(string(c))
		case c == '&' && i+1 < len(runes) && runes[i+1] == '&':
			pushOp("&&")
			i++
		case c == '|' && i+1 < len(runes) && runes[i+1] == '|':
			pushOp("||")
			i++
		case c == '|':
			pushOp("|")
		case c == '&' && i+1 < len(runes) && runes[i+1] == '>':
			pushOp("&>")
			i++
		case c == '&':
			pushOp("&")
		case c == '>':
			fd := buf.String()
			if fd == "1" || fd == "2" {
				buf.Reset()
			}
			op := ">"
			if i+1 < len(runes) && runes[i+1] == '>' {
				op = ">>"
				i++
			}
			if i+1 < len(runes) && runes[i+1] == '&' {
				for i++; i+1 < len(runes) && runes[i+1] >= '0' && runes[i+1] <= '9'; i++ {
				}
				flush()
				continue
			}
			pushOp(op)
		case c == '<':
			flush()
			toks = append(toks, token{text: "<", op: true})
		default:
			buf.WriteRune(c)
		}
	}
	flush()
	return toks
}

var separators = map[string]bool{"\n": true, ";": true, "|": true, "||": true, "&&": true, "&": true, "(": true, ")": true}

func splitSegments(toks []token) [][]token {
	var segs [][]token
	var cur []token
	for _, t := range toks {
		if t.op && separators[t.text] {
			if len(cur) > 0 {
				segs = append(segs, cur)
			}
			cur = nil
			continue
		}
		cur = append(cur, t)
	}
	if len(cur) > 0 {
		segs = append(segs, cur)
	}
	return segs
}

func splitRedirects(seg []token) (argv, redirects []string) {
	for i := 0; i < len(seg); i++ {
		t := seg[i]
		if t.op && (t.text == ">" || t.text == ">>" || t.text == "&>") {
			if i+1 < len(seg) && !seg[i+1].op {
				redirects = append(redirects, seg[i+1].text)
				i++
			}
			continue
		}
		if t.op && t.text == "<" {
			if i+1 < len(seg) && !seg[i+1].op {
				i++
			}
			continue
		}
		if !t.op {
			argv = append(argv, t.text)
		}
	}
	return argv, redirects
}
