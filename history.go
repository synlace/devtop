package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/adrg/frontmatter"
)

// Revision is a single commit that touched a document (or ticket), newest
// first. The documented/deleted status is resolved from the commit itself.
type Revision struct {
	SHA       string `json:"sha"`
	Short     string `json:"short"`
	Message   string `json:"message"`
	Author    string `json:"author"`
	Date      string `json:"date"`
	IsCurrent bool   `json:"is_current,omitempty"`
}

func execGit(repoRoot string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return "", fmt.Errorf("git: %v", msg)
		}
		return "", err
	}
	return string(out), nil
}

// findRepoRoot returns the git repository devtop commits to, mirroring the
// agent's gitCommit convention: the workspace root is the parent of DEVTOP_DIR
// (the standard <workspace>/.devtop layout). It also accepts DEVTOP_DIR itself
// being the repo (e.g. DEVTOP_DIR set to the workspace root). It never climbs
// beyond those two, so an unrelated ancestor repo (a $HOME or /tmp checkout)
// is never mistaken for the workspace.
func findRepoRoot() (string, error) {
	return findRepoRootFrom(DEVTOP_DIR)
}

func findRepoRootFrom(devTopDir string) (string, error) {
	abs, err := filepath.Abs(devTopDir)
	if err != nil {
		return "", err
	}
	candidates := []string{filepath.Dir(abs), abs}
	for _, dir := range candidates {
		if fi, err := os.Stat(filepath.Join(dir, ".git")); err == nil && fi.IsDir() {
			return dir, nil
		}
	}
	return "", fmt.Errorf("not inside a git repository: no .git found above %s", devTopDir)
}

// gitRelPath converts an absolute file path (e.g. the resolved doc file) into
// the path git knows it by, relative to the repository root.
func gitRelPath(absPath string) (string, error) {
	root, err := findRepoRoot()
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, absPath)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}

func gitRelPathFrom(root, absPath string) (string, error) {
	rel, err := filepath.Rel(root, absPath)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}

// docPathForSlug resolves a doc slug to the real file path, mirroring the
// non-slash -> <slug>/index.mdx fallback used by getDoc.
func docPathForSlug(slug string) (string, error) {
	return docPathForSlugP(defaultPaths(), slug)
}

func docPathForSlugP(p RepoPaths, slug string) (string, error) {
	slug = strings.TrimSuffix(slug, ".mdx")
	if _, err := guardPath(p.Docs, slug); err != nil {
		return "", err
	}
	q := filepath.Join(p.Docs, slug+".mdx")
	if _, err := os.Stat(q); os.IsNotExist(err) {
		q = filepath.Join(p.Docs, slug, "index.mdx")
	}
	if _, err := os.Stat(q); err != nil {
		return "", fmt.Errorf("document not found: %s", slug)
	}
	return q, nil
}

func listRevisions(absPath string) ([]Revision, error) {
	rel, err := gitRelPath(absPath)
	if err != nil {
		return nil, err
	}
	root, _ := findRepoRoot()
	return listRevisionsIn(root, rel)
}

func listRevisionsIn(root, rel string) ([]Revision, error) {
	out, err := execGit(root, "log", "--format=%H%x00%h%x00%s%x00%an%x00%aI", "--", rel)
	if err != nil {
		return nil, fmt.Errorf("no git history for %s: %v", rel, err)
	}
	var revs []Revision
	if strings.TrimSpace(out) == "" {
		return revs, nil
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		parts := strings.Split(line, "\x00")
		sha := ""
		short := ""
		msg := ""
		author := ""
		date := ""
		if len(parts) > 0 {
			sha = parts[0]
		}
		if len(parts) > 1 {
			short = parts[1]
		}
		if len(parts) > 2 {
			msg = parts[2]
		}
		if len(parts) > 3 {
			author = parts[3]
		}
		if len(parts) > 4 {
			date = parts[4]
		}
		revs = append(revs, Revision{SHA: sha, Short: short, Message: msg, Author: author, Date: date})
	}
	if len(revs) > 0 {
		revs[0].IsCurrent = true
	}
	return revs, nil
}

// contentAt returns the file's contents at a specific commit (rendered body
// via frontmatter, like getDoc) plus whether the file was deleted there.
// The sha is validated first so a bad commit is an error, not "deleted".
func contentAt(absPath, sha string) (title, content string, deleted bool, err error) {
	root, err := findRepoRoot()
	if err != nil {
		return "", "", false, err
	}
	rel, err := gitRelPathFrom(root, absPath)
	if err != nil {
		return "", "", false, err
	}
	return contentAtIn(root, rel, sha)
}

func contentAtIn(root, rel, sha string) (title, content string, deleted bool, err error) {
	if _, err := execGit(root, "rev-parse", "--verify", "--quiet", sha+"^{commit}"); err != nil {
		return "", "", false, fmt.Errorf("invalid commit: %s", sha)
	}
	out, err := execGit(root, "show", sha+":"+rel)
	if err != nil {
		// The commit exists but the file is absent at it (deleted/renamed).
		return "", "", true, nil
	}
	var meta DocMeta
	bodyBytes, parseErr := frontmatter.Parse(strings.NewReader(out), &meta)
	if parseErr != nil {
		return strings.TrimSuffix(filepath.Base(rel), filepath.Ext(rel)), strings.TrimSpace(out), false, nil
	}
	if meta.Title == "" {
		meta.Title = strings.TrimSuffix(filepath.Base(rel), filepath.Ext(rel))
	}
	return meta.Title, strings.TrimSpace(string(bodyBytes)), false, nil
}

// diffBetween returns the raw unified diff (git) between two commits for the
// file. git-diff-view consumes this verbatim.
func diffBetween(absPath, from, to string) (string, error) {
	root, err := findRepoRoot()
	if err != nil {
		return "", err
	}
	rel, err := gitRelPathFrom(root, absPath)
	if err != nil {
		return "", err
	}
	return diffBetweenIn(root, rel, from, to)
}

func diffBetweenIn(root, rel, from, to string) (string, error) {
	return execGit(root, "diff", "--no-color", "--unified=3", from, to, "--", rel)
}
