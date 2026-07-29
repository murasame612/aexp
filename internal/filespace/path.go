package filespace

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/ziwu/aexp/internal/store"
)

const Scheme = "aexp"

var workspacePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// LogicalPath is the stable identity of a managed file or directory. It never
// contains a host name, credential, or machine-specific absolute path.
type LogicalPath struct {
	Workspace string
	Path      string
}

// Parse validates and canonicalizes an aexp logical URI.
func Parse(raw string) (LogicalPath, error) {
	if strings.TrimSpace(raw) != raw || raw == "" {
		return LogicalPath{}, fmt.Errorf("logical URI must not be empty or padded with whitespace")
	}
	if hasUnsafeText(raw) || strings.Contains(raw, `\`) {
		return LogicalPath{}, fmt.Errorf("logical URI contains unsafe characters")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return LogicalPath{}, fmt.Errorf("parse logical URI: %w", err)
	}
	if u.Scheme != Scheme || u.Opaque != "" || u.User != nil || u.Port() != "" || u.RawQuery != "" || u.Fragment != "" {
		return LogicalPath{}, fmt.Errorf("logical URI must use aexp://workspace/path without user, port, query, or fragment")
	}
	if !workspacePattern.MatchString(u.Host) {
		return LogicalPath{}, fmt.Errorf("invalid logical workspace %q", u.Host)
	}
	decoded, err := url.PathUnescape(u.EscapedPath())
	if err != nil {
		return LogicalPath{}, fmt.Errorf("decode logical path: %w", err)
	}
	decoded = strings.TrimPrefix(decoded, "/")
	if hasUnsafeText(decoded) || strings.Contains(decoded, `\`) {
		return LogicalPath{}, fmt.Errorf("logical path contains unsafe characters")
	}
	for _, segment := range strings.Split(decoded, "/") {
		if segment == ".." {
			return LogicalPath{}, fmt.Errorf("logical path must not contain ..")
		}
	}
	cleaned := path.Clean("/" + decoded)
	cleaned = strings.TrimPrefix(cleaned, "/")
	if cleaned == "" || cleaned == "." {
		return LogicalPath{}, fmt.Errorf("logical path must not be empty")
	}
	return LogicalPath{Workspace: u.Host, Path: cleaned}, nil
}

func (p LogicalPath) String() string {
	u := url.URL{Scheme: Scheme, Host: p.Workspace, Path: "/" + p.Path}
	return u.String()
}

// RelativeTo returns the suffix below prefix. An exact match returns an empty
// suffix; a sibling such as data2 never matches data.
func (p LogicalPath) RelativeTo(prefix string) (string, bool) {
	prefix = strings.Trim(path.Clean("/"+prefix), "/")
	if prefix == "" || prefix == "." {
		return "", false
	}
	if p.Path == prefix {
		return "", true
	}
	if strings.HasPrefix(p.Path, prefix+"/") {
		return strings.TrimPrefix(p.Path, prefix+"/"), true
	}
	return "", false
}

// ResolveRoot selects the unique longest matching root for a logical path.
// Save-time validation should prevent overlaps, but this function still
// rejects equally specific ambiguity instead of silently choosing one.
func ResolveRoot(p LogicalPath, roots []store.LogicalRoot) (store.LogicalRoot, string, error) {
	type match struct {
		root store.LogicalRoot
		rel  string
	}
	matches := make([]match, 0, len(roots))
	for _, root := range roots {
		if root.Workspace != p.Workspace {
			continue
		}
		if rel, ok := p.RelativeTo(root.Prefix); ok {
			matches = append(matches, match{root: root, rel: rel})
		}
	}
	if len(matches) == 0 {
		return store.LogicalRoot{}, "", fmt.Errorf("no logical root matches %s", p.String())
	}
	sort.Slice(matches, func(i, j int) bool { return len(matches[i].root.Prefix) > len(matches[j].root.Prefix) })
	if len(matches) > 1 && len(matches[0].root.Prefix) == len(matches[1].root.Prefix) {
		return store.LogicalRoot{}, "", fmt.Errorf("ambiguous logical root for %s", p.String())
	}
	return matches[0].root, matches[0].rel, nil
}

// PhysicalPath joins a root-relative storage path and logical suffix while
// preserving the configured storage-root boundary.
func PhysicalPath(root store.LogicalRoot, suffix string) (string, error) {
	base := strings.Trim(path.Clean("/"+root.PhysicalRoot), "/")
	if base == "" || base == "." || strings.HasPrefix(root.PhysicalRoot, "/") || containsParent(root.PhysicalRoot) {
		return "", fmt.Errorf("logical root physical path must be relative and contained")
	}
	if strings.HasPrefix(suffix, "/") || containsParent(suffix) || hasUnsafeText(suffix) || strings.Contains(suffix, `\`) {
		return "", fmt.Errorf("logical suffix escapes physical root")
	}
	joined := path.Clean(path.Join(base, suffix))
	if joined != base && !strings.HasPrefix(joined, base+"/") {
		return "", fmt.Errorf("resolved path escapes physical root")
	}
	return joined, nil
}

func containsParent(value string) bool {
	for _, segment := range strings.Split(value, "/") {
		if segment == ".." {
			return true
		}
	}
	return false
}

func hasUnsafeText(value string) bool {
	return !utf8.ValidString(value) || strings.ContainsAny(value, "\x00\r\n")
}
