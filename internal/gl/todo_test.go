package gl

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// todoPage is a page of n To-Dos on issues of acme, each with pad bytes
// of a field the decoder ignores.
func todoPage(n, pad int) string {
	var b strings.Builder
	b.WriteString("[")
	for i := range n {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"id":%d,"action_name":"mentioned","target_type":"Issue","project":{"path_with_namespace":"acme/x"},"target":{"iid":%d},"pad":"%s"}`,
			i+1, i+1, strings.Repeat("p", pad))
	}
	b.WriteString("]")
	return b.String()
}

// MaxTodoPages full pages may be exactly 1000 To-Dos: the one after them
// is asked for alone, and only its presence marks the list truncated.
func TestPendingIssueTodosExactlyFull(t *testing.T) {
	probe := fmt.Sprintf("page=%d&per_page=1&", MaxTodoPages*100+1)
	for _, tc := range []struct {
		more      string
		truncated bool
	}{{"[]", false}, {todoPage(1, 0), true}} {
		log := scriptGlab(t, `case "$*" in *'`+probe+`'*) echo '`+tc.more+`';; *) echo '`+todoPage(100, 0)+`';; esac`)
		todos, trunc, err := Client{}.PendingIssueTodos(context.Background(), 42, "acme")
		if err != nil || len(todos) != 100*MaxTodoPages || trunc != tc.truncated {
			t.Errorf("one more = %s: %d To-Dos, truncated %v, %v; want %d, %v", tc.more, len(todos), trunc, err, 100*MaxTodoPages, tc.truncated)
		}
		if calls := readFile(t, log); strings.Count(calls, "todos?") != MaxTodoPages+1 || !strings.Contains(calls, probe) {
			t.Errorf("requests:\n%s", calls)
		}
	}
}

// The pages of one fetch share a single MaxOutput: a server cannot push
// MaxTodoPages times that much by keeping every page under the cap.
func TestPendingIssueTodosCapsAllPages(t *testing.T) {
	old := MaxOutput
	MaxOutput = 64 << 10
	t.Cleanup(func() { MaxOutput = old })
	page := todoPage(100, 300) // about 40 KB: under the cap alone, over it with the next
	log := scriptGlab(t, `echo '`+page+`'`)
	_, _, err := Client{}.PendingIssueTodos(context.Background(), 42, "acme")
	if !errors.Is(err, ErrTooLarge) || !strings.Contains(err.Error(), "GET todos") {
		t.Fatalf("want ErrTooLarge naming the request, got %v", err)
	}
	if n := strings.Count(readFile(t, log), "todos?"); n != 2 {
		t.Errorf("%d requests, want 2: the fetch must stop at the cap", n)
	}
}
