// Package scan finds repository roots and git repositories under them.
package scan

import (
	"os"
	"path/filepath"
	"sort"
)

// FindRepos returns every directory under the given roots that contains a
// .git directory, sorted by path.
func FindRepos(roots []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || !d.IsDir() {
				return nil
			}
			if d.Name() == ".git" {
				out = append(out, filepath.Dir(path))
				return filepath.SkipDir
			}
			if d.Name() == "node_modules" || d.Name() == "target" ||
				d.Name() == "vendor" || d.Name() == ".cache" {
				return filepath.SkipDir
			}
			return nil
		})
	}
	sort.Strings(out)
	unique := out[:0]
	for _, p := range out {
		if !seen[p] {
			seen[p] = true
			unique = append(unique, p)
		}
	}
	return unique
}
