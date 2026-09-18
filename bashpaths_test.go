package main

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func TestBashWriteTargets(t *testing.T) {
	cwd := t.TempDir()
	os.MkdirAll(filepath.Join(cwd, ".git"), 0o755)
	os.MkdirAll(filepath.Join(cwd, "pkg"), 0o755)
	os.WriteFile(filepath.Join(cwd, "pkg", "a.go"), nil, 0o644)
	os.WriteFile(filepath.Join(cwd, "pkg", "b.go"), nil, 0o644)
	abs := func(p string) string { return filepath.Join(cwd, p) }

	cases := []struct {
		name      string
		cmd       string
		want      []string
		forbidden bool
	}{
		{"read only", "cat pkg/a.go | grep foo", nil, false},
		{"redirect", "echo hi > out.txt", []string{abs("out.txt")}, false},
		{"append redirect with fd", "cmd 2>> log.txt", []string{abs("log.txt")}, false},
		{"stderr dup ignored", "go test ./... 2>&1", nil, false},
		{"devnull ignored", "cmd > /dev/null", nil, false},
		{"sed in place", "sed -i '' 's/a/b/' pkg/a.go", []string{abs("pkg/a.go")}, false},
		{"sed in place with -e", "sed -i -e 's/a/b/' pkg/a.go pkg/b.go", []string{abs("pkg/a.go"), abs("pkg/b.go")}, false},
		{"sed without -i", "sed 's/a/b/' pkg/a.go", nil, false},
		{"cd then write", "cd pkg && echo x > c.go", []string{abs("pkg/c.go")}, false},
		{"heredoc body ignored", "cat > gen.go <<'EOF'\necho this > not-a-target\nEOF\n", []string{abs("gen.go")}, false},
		{"tee", "make 2>&1 | tee build.log", []string{abs("build.log")}, false},
		{"mv both", "mv pkg/a.go pkg/z.go", []string{abs("pkg/a.go"), abs("pkg/z.go")}, false},
		{"cp dest only", "cp pkg/a.go pkg/copy.go", []string{abs("pkg/copy.go")}, false},
		{"glob expands", "gofmt -w pkg/*.go", []string{abs("pkg/a.go"), abs("pkg/b.go")}, false},
		{"gofmt without -w", "gofmt -l pkg/*.go", nil, false},
		{"rm absolute recursive ok", "rm -rf " + abs("pkg"), []string{abs("pkg")}, false},
		{"rm relative recursive forbidden", "rm -rf pkg", nil, true},
		{"rm recursive checkout root forbidden", "rm -rf " + cwd, nil, true},
		{"rm recursive ancestor of checkout forbidden", "rm -rf " + filepath.Dir(cwd), nil, true},
		{"rm recursive slash forbidden", "rm -rf /", nil, true},
		{"rm relative file ok", "rm pkg/a.go", []string{abs("pkg/a.go")}, false},
		{"git update-ref forbidden", "git update-ref refs/heads/main abc", nil, true},
		{"git checkout paths", "git checkout -- pkg/a.go", []string{abs("pkg/a.go")}, false},
		{"git checkout branch", "git checkout main", nil, false},
		{"git restore", "git restore --source HEAD pkg/a.go", []string{abs("pkg/a.go")}, false},
		{"git restore dot", "git restore .", []string{cwd}, false},
		{"git checkout dot from subdir", "cd pkg && git checkout -- .", []string{abs("pkg")}, false},
		{"git stash", "git stash", []string{cwd}, false},
		{"git stash pop from subdir", "cd pkg && git stash pop", []string{cwd}, false},
		{"git stash list", "git stash list", nil, false},
		{"git reset hard", "git reset --hard HEAD~1", []string{cwd}, false},
		{"git reset mixed", "git reset HEAD pkg/a.go", nil, false},
		{"git clean force", "git clean -fd", []string{cwd}, false},
		{"git clean dry run", "git clean -n", nil, false},
		{"git clean combined dry run", "git clean -fdn", nil, false},
		{"env prefix", "GOFLAGS=-mod=mod sudo tee x.txt", []string{abs("x.txt")}, false},
		{"quoted path with space", `echo x > "my file.txt"`, []string{abs("my file.txt")}, false},
		{"python inline invisible", "python3 - <<'EOF'\nopen('x','w')\nEOF", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, forbidden := bashWriteTargets(tc.cmd, cwd)
			if (forbidden != "") != tc.forbidden {
				t.Fatalf("forbidden=%q want forbidden=%v", forbidden, tc.forbidden)
			}
			sort.Strings(got)
			want := append([]string(nil), tc.want...)
			sort.Strings(want)
			if len(got) == 0 && len(want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got %v want %v", got, want)
			}
		})
	}
}
