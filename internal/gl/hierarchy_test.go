package gl

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// scriptGlab puts a fake glab first in PATH that runs the shell body,
// with the request body (stdin) in $in. It logs one line per call.
func scriptGlab(t *testing.T, body string) (log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "calls")
	script := "#!/bin/sh\nin=$(cat)\nprintf '%s %s\\n' \"$*\" \"$in\" >> '" + log + "'\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, "glab"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

const page1 = `{"data":{"group":{"workItems":{"pageInfo":{"hasNextPage":true,"endCursor":"c1"},"nodes":[` +
	`{"id":"gid://gitlab/WorkItem/11","widgets":[{},{"parent":{"id":"gid://gitlab/WorkItem/10"}}]},` +
	`{"id":"gid://gitlab/WorkItem/10","widgets":[{"parent":null}]}]}}}}`
const page2 = `{"data":{"group":{"workItems":{"pageInfo":{"hasNextPage":false,"endCursor":"c2"},"nodes":[` +
	`{"id":"gid://gitlab/WorkItem/12","widgets":[{"parent":{"id":"gid://gitlab/WorkItem/10"}}]},` +
	`{"id":"bogus","widgets":[{"parent":{"id":"gid://gitlab/WorkItem/10"}}]}]}}}}`

func TestWorkItemParentsPaginates(t *testing.T) {
	log := scriptGlab(t, `case "$in" in *'"after":"c1"'*) echo '`+page2+`';; *) echo '`+page1+`';; esac`)
	got, trunc, err := Client{Host: "gitlab.example.edu"}.WorkItemParents(context.Background(), "acme/platform", "opened")
	if err != nil || trunc {
		t.Fatalf("err %v, truncated %v", err, trunc)
	}
	if len(got) != 2 || got[11] != 10 || got[12] != 10 {
		t.Errorf("parents = %v", got)
	}
	b, _ := os.ReadFile(log)
	calls := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(calls) != 2 {
		t.Fatalf("%d requests, want 2:\n%s", len(calls), b)
	}
	for _, c := range calls {
		if !strings.Contains(c, "--method POST") || !strings.Contains(c, " graphql ") ||
			!strings.Contains(c, `"fullPath":"acme/platform"`) || !strings.Contains(c, `"state":"opened"`) {
			t.Errorf("request: %s", c)
		}
	}
}

func TestWorkItemParentsErrors(t *testing.T) {
	for name, out := range map[string]string{
		"graphql error": `{"errors":[{"message":"Field 'workItems' doesn't accept argument 'includeDescendants'\u001b[2J"}]}`,
		"no group":      `{"data":{"group":null}}`,
		"no workItems":  `{"data":{"group":{}}}`,
		"not json":      `<html>`,
	} {
		scriptGlab(t, `echo '`+out+`'`)
		_, _, err := Client{}.WorkItemParents(context.Background(), "acme/platform", "all")
		if err == nil {
			t.Errorf("%s: want an error", name)
		} else if strings.ContainsRune(err.Error(), '\x1b') {
			t.Errorf("%s: unsanitized error %q", name, err)
		}
	}
	scriptGlab(t, `echo 'HTTP 404' >&2; exit 1`)
	if _, _, err := (Client{}).WorkItemParents(context.Background(), "acme/platform", "all"); err == nil {
		t.Error("failed glab: want an error")
	}
}

func TestWorkItemParentsTruncates(t *testing.T) {
	scriptGlab(t, `echo '`+strings.ReplaceAll(page1, `"endCursor":"c1"`, `"endCursor":"more"`)+`'`)
	got, trunc, err := Client{}.WorkItemParents(context.Background(), "acme/platform", "opened")
	if err != nil || !trunc || got[11] != 10 {
		t.Errorf("got %v, truncated %v, err %v", got, trunc, err)
	}
}

func TestGlobalID(t *testing.T) {
	for gid, want := range map[string]int{
		"gid://gitlab/WorkItem/123": 123,
		"gid://gitlab/Issue/7":      7,
		"gid://gitlab/WorkItem/0":   0,
		"gid://gitlab/WorkItem/-4":  0,
		"gid://gitlab/WorkItem/":    0,
		"gid://gitlab/WorkItem/1x":  0,
		"gitlab/WorkItem/5":         0,
		"123":                       0,
		"":                          0,
	} {
		n, ok := globalID(gid)
		if ok != (want > 0) || ok && n != want {
			t.Errorf("globalID(%q) = %d, %v; want %d", gid, n, ok, want)
		}
	}
}

// The pages of one fetch share a single MaxOutput: a server cannot push
// MaxParentPages times that much by keeping every page under the cap.
func TestWorkItemParentsCapsAllPages(t *testing.T) {
	old := MaxOutput
	MaxOutput = 1 << 10
	t.Cleanup(func() { MaxOutput = old })
	more := strings.ReplaceAll(page1, `"endCursor":"c1"`, `"endCursor":"more"`) // never the last page
	log := scriptGlab(t, `echo '`+more+`'`)
	_, _, err := Client{}.WorkItemParents(context.Background(), "acme/platform", "opened")
	if !errors.Is(err, ErrTooLarge) || !strings.Contains(err.Error(), "POST graphql") {
		t.Fatalf("want ErrTooLarge naming the request, got %v", err)
	}
	if n := strings.Count(readFile(t, log), "\n"); n >= MaxParentPages {
		t.Errorf("%d requests: the fetch must stop at the cap", n)
	}
}
