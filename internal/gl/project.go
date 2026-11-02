package gl

import (
	"context"
	"net/url"
	"sort"
	"strings"
)

// Projects of a group: listing them and ranking them for the picker and
// the `new` subcommand.

// Project is a GitLab project issues can be filed in.
type Project struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Path string `json:"path_with_namespace"` // full path, e.g. "team/sub/proj"
}

// GroupProjects lists the non-archived projects with issues enabled in a
// group and all its subgroups.
func (c Client) GroupProjects(ctx context.Context, group string) ([]Project, error) {
	q := url.Values{
		"include_subgroups":   {"true"},
		"archived":            {"false"},
		"with_issues_enabled": {"true"},
		"simple":              {"true"},
		"per_page":            {"100"},
	}
	b, err := c.API(ctx, "GET", "groups/"+PathID(group)+"/projects?"+q.Encode(), nil, true)
	if err != nil {
		return nil, err
	}
	return decodeList[Project](b)
}

// Base returns the last segment of the project path, e.g. "proj" for
// "team/sub/proj".
func (p Project) Base() string { return p.Path[strings.LastIndex(p.Path, "/")+1:] }

// RankProjects returns the projects matching query, best first. Every
// whitespace-separated word of query must occur in the project path or
// its display name (case-insensitive). Matches on the last path segment
// or the display name rank above other path matches; boost breaks
// ties (higher first, e.g. recently used projects), then the path.
// An empty query matches everything.
func RankProjects(projects []Project, query string, boost func(Project) int) []Project {
	words := strings.Fields(strings.ToLower(query))
	type scored struct {
		p            Project
		score, boost int
	}
	var out []scored
next:
	for _, p := range projects {
		path, base := strings.ToLower(p.Path), strings.ToLower(p.Base())
		display := strings.ToLower(p.Name)
		score := 0
		for _, w := range words {
			if !strings.Contains(path, w) && !strings.Contains(display, w) {
				continue next
			}
			switch {
			case base == w:
				score += 100
			case strings.HasPrefix(base, w):
				score += 50
			case strings.Contains(base, w) || strings.Contains(display, w):
				score += 20
			}
		}
		b := 0
		if boost != nil {
			b = boost(p)
		}
		out = append(out, scored{p, score, b})
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.score != b.score {
			return a.score > b.score
		}
		if a.boost != b.boost {
			return a.boost > b.boost
		}
		return a.p.Path < b.p.Path
	})
	ps := make([]Project, len(out))
	for i, s := range out {
		ps[i] = s.p
	}
	return ps
}

// RelPath returns path relative to group ("acme/platform/web/site" in
// "acme/platform" is "web/site"); ok is false when path is not inside
// group. GitLab paths are case-insensitive, so a group typed as
// "Acme/Platform" still matches the canonical "acme/platform/…".
func RelPath(path, group string) (rel string, ok bool) {
	n := len(group)
	if group == "" || len(path) <= n || path[n] != '/' || !strings.EqualFold(path[:n], group) {
		return path, false
	}
	return path[n+1:], true
}

// InGroup reports whether path is group itself or inside it, ignoring
// case.
func InGroup(path, group string) bool {
	_, ok := RelPath(path, group)
	return ok || strings.EqualFold(path, group)
}
