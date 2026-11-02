package gl

import (
	"context"
	"fmt"
	"net/url"
)

// Todo is one of the user's pending GitLab To-Do items on an issue.
type Todo struct {
	ID         int    `json:"id"`
	ActionName string `json:"action_name"` // "mentioned", "assigned", "directly_addressed", …
	TargetType string `json:"target_type"`
	Project    struct {
		Path string `json:"path_with_namespace"`
	} `json:"project"`
	Target struct {
		IID int `json:"iid"`
	} `json:"target"`
}

func (t *Todo) sanitize() {
	t.ActionName, t.TargetType = SanitizeLine(t.ActionName), SanitizeLine(t.TargetType)
	t.Project.Path = SanitizeLine(t.Project.Path)
}

// Key identifies the issue a To-Do is about, like IssueKey.
func (t Todo) Key() string { return IssueKey(t.Project.Path, t.Target.IID) }

// IssueKey identifies an issue by project path and iid ("acme/x#3").
func IssueKey(project string, iid int) string { return fmt.Sprintf("%s#%d", project, iid) }

// MaxTodoPages bounds the pages (of 100 To-Dos) PendingIssueTodos reads
// per call.
const MaxTodoPages = 10

// PendingIssueTodos returns the user's pending To-Dos on issues in the
// group with ID groupID and its subgroups, page by page (100 each, at
// most MaxTodoPages; the pages together are capped at MaxOutput).
// truncated is true when more To-Dos exist; the list then holds the
// first ones. group is the group's path: To-Dos outside it are dropped
// too, in case the server does not filter by group. Works with a
// read_api token; it never marks anything done.
func (c Client) PendingIssueTodos(ctx context.Context, groupID int, group string) (todos []Todo, truncated bool, err error) {
	var total int64
	fetch := func(perPage, page int) ([]Todo, error) {
		q := url.Values{"state": {"pending"}, "type": {"Issue"}, "group_id": {fmt.Sprint(groupID)},
			"per_page": {fmt.Sprint(perPage)}, "page": {fmt.Sprint(page)}}
		b, err := c.API(ctx, "GET", "todos?"+q.Encode(), nil, false)
		if err != nil {
			return nil, err
		}
		if err := addPage(&total, b, "GET todos"); err != nil {
			return nil, err
		}
		return decodeList[Todo](b)
	}
	for page := 1; page <= MaxTodoPages; page++ {
		all, err := fetch(100, page)
		if err != nil {
			return nil, false, err
		}
		for _, t := range all {
			if t.TargetType == "Issue" && InGroup(t.Project.Path, group) {
				todos = append(todos, t)
			}
		}
		if len(all) < 100 { // the last page
			return todos, false, nil
		}
	}
	// MaxTodoPages full pages may be all there is: ask for the one To-Do
	// after them alone, rather than report exactly 1000 as "1000+". It is
	// not filtered: any To-Do past the pages may hide more issue ones.
	more, err := fetch(1, MaxTodoPages*100+1)
	if err != nil {
		return nil, false, err
	}
	return todos, len(more) > 0, nil
}
