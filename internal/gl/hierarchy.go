package gl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Work item hierarchy: which issue (or task) is a child of which. The
// REST issue list has no parent field, so it comes from one GraphQL query
// over the group's work items, paginated. A work item's global ID ends in
// the same number as the issue's REST id.

// parentsQuery lists every work item of the group and its descendant
// groups and projects with its parent, if any. Only IDs are read: no
// text from it is ever shown.
const parentsQuery = `query($fullPath: ID!, $state: IssuableState, $after: String) {
  group(fullPath: $fullPath) {
    workItems(includeDescendants: true, state: $state, first: 100, after: $after) {
      pageInfo { hasNextPage endCursor }
      nodes { id widgets { ... on WorkItemWidgetHierarchy { parent { id } } } }
    }
  }
}`

// MaxParentPages bounds the pages (of 100 work items) WorkItemParents
// reads per call.
const MaxParentPages = 50

type parentsPage struct {
	Data struct {
		Group *struct {
			WorkItems *struct {
				PageInfo struct {
					HasNextPage bool   `json:"hasNextPage"`
					EndCursor   string `json:"endCursor"`
				} `json:"pageInfo"`
				Nodes []struct {
					ID      string `json:"id"`
					Widgets []struct {
						Parent *struct {
							ID string `json:"id"`
						} `json:"parent"`
					} `json:"widgets"`
				} `json:"nodes"`
			} `json:"workItems"`
		} `json:"group"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// ErrNoWorkItems is returned when the server answers but the group has
// no work item list (an older GitLab, or no access).
var ErrNoWorkItems = errors.New("this GitLab does not list the group's work items")

// WorkItemParents maps the ID of each work item of group (subgroups and
// projects included) in state ("opened", "closed" or "all") that has a
// parent to the parent's ID. IDs are the issues' REST ids. truncated is
// true when more than MaxParentPages pages exist; the map then holds the
// first ones. The pages together are capped at MaxOutput. Needs only
// read_api.
func (c Client) WorkItemParents(ctx context.Context, group, state string) (parents map[int]int, truncated bool, err error) {
	parents = map[int]int{}
	after := ""
	var total int64
	for page := 0; page < MaxParentPages; page++ {
		vars := map[string]any{"fullPath": group, "state": state}
		if after != "" {
			vars["after"] = after
		}
		b, err := c.API(ctx, "POST", "graphql", map[string]any{"query": parentsQuery, "variables": vars}, false)
		if err != nil {
			return nil, false, err
		}
		if err := addPage(&total, b, "POST graphql"); err != nil {
			return nil, false, err
		}
		var p parentsPage
		if err := json.Unmarshal(b, &p); err != nil {
			return nil, false, fmt.Errorf("work items: %w", err)
		}
		if len(p.Errors) > 0 {
			return nil, false, fmt.Errorf("work items: %s", cleanErr(p.Errors[0].Message))
		}
		if p.Data.Group == nil || p.Data.Group.WorkItems == nil {
			return nil, false, ErrNoWorkItems
		}
		wi := p.Data.Group.WorkItems
		for _, n := range wi.Nodes {
			id, ok := globalID(n.ID)
			if !ok {
				continue
			}
			for _, w := range n.Widgets {
				if w.Parent == nil {
					continue
				}
				if pid, ok := globalID(w.Parent.ID); ok && pid != id {
					parents[id] = pid
				}
			}
		}
		if !wi.PageInfo.HasNextPage || wi.PageInfo.EndCursor == "" {
			return parents, false, nil
		}
		after = wi.PageInfo.EndCursor
	}
	return parents, true, nil
}

// globalID returns the number at the end of a GraphQL global ID such as
// "gid://gitlab/WorkItem/123".
func globalID(gid string) (int, bool) {
	i := strings.LastIndexByte(gid, '/')
	if !strings.HasPrefix(gid, "gid://") || i < 0 {
		return 0, false
	}
	n, err := strconv.Atoi(gid[i+1:])
	return n, err == nil && n > 0
}
