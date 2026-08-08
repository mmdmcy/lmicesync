// Package sync fetches peers' auto branches (as git bundles over the LAN
// agent) and merges them into a local auto/merged branch. Conflicts are
// reported, never auto-resolved. Nothing touches public remotes.
package sync

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mmdmcy/lmicesync/internal/config"
	"github.com/mmdmcy/lmicesync/internal/scan"
	"github.com/mmdmcy/lmicesync/internal/serve"
)

func run(dir, name string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir, name}, args...)...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func trim(s string) string {
	return strings.TrimSpace(s)
}

// fetchBundle downloads a peer's bundle for a repo and fetches its auto refs
// into refs/peers/<device>/...
func fetchBundle(dir, url, device, token string) error {
	bundle, err := os.CreateTemp("", "lmicesync-inbound-*")
	if err != nil {
		return err
	}
	defer os.Remove(bundle.Name())

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("peer %s: %s: %s", device, resp.Status, strings.TrimSpace(string(body)))
	}
	if _, err := io.Copy(bundle, resp.Body); err != nil {
		return err
	}
	bundle.Close()

	refspec := fmt.Sprintf("+refs/heads/auto/*:refs/peers/%s/auto/*", device)
	if _, err := run(dir, "fetch", bundle.Name(), refspec); err != nil {
		return fmt.Errorf("peer %s: %v", device, err)
	}
	return nil
}

// mergedRef returns the auto/merged branch's head, or "" if it does not exist.
func mergedRef(dir string) string {
	out, err := run(dir, "rev-parse", "--verify", "-q", "refs/heads/auto/merged")
	if err != nil {
		return ""
	}
	return trim(out)
}

// mergeTree merges two commits and returns the resulting tree (or error with
// conflict file names).
func mergeTree(dir, base, ours, theirs string) (string, error) {
	out, err := run(dir, "merge-tree", "--write-tree", "--name-only", base, ours, theirs)
	if err != nil {
		lines := strings.Split(strings.TrimSpace(out), "\n")
		conflicts := []string{}
		for _, l := range lines {
			if l != "" {
				conflicts = append(conflicts, l)
			}
		}
		return "", fmt.Errorf("conflicts: %s", strings.Join(conflicts, ", "))
	}
	return trim(out), nil
}

// ensureMergedExists creates auto/merged as a copy of the local auto branch
// when it does not exist yet, and returns its head. When the machine has no
// local auto branch yet, mergedHead is "" and the caller may seed from a
// peer instead.
func ensureMergedExists(dir string) (string, error) {
	if head := mergedRef(dir); head != "" {
		return head, nil
	}
	own, err := run(dir, "rev-parse", "--verify", "-q", "refs/heads/auto/"+localDevice())
	if err != nil || trim(own) == "" {
		return "", nil // no local auto branch yet; seed from a peer
	}
	if _, err := run(dir, "update-ref", "refs/heads/auto/merged", trim(own)); err != nil {
		return "", err
	}
	return trim(own), nil
}

// mergePeer merges every auto branch fetched from the peer into
// auto/merged. A conflict aborts the merge and is reported; auto/merged
// stays untouched so the operator can resolve manually later.
func mergePeer(dir, device string) (int, error) {
	refsOut, err := run(dir, "for-each-ref", "--format=%(refname)",
		"refs/peers/"+device+"/auto/")
	if err != nil || strings.TrimSpace(refsOut) == "" {
		return 0, nil // peer has no auto refs for this repo
	}
	mergedCount := 0
	for _, peerRef := range strings.Fields(refsOut) {
		peerHead, err := run(dir, "rev-parse", "--verify", "-q", peerRef)
		if err != nil || trim(peerHead) == "" {
			continue
		}
		peerHead = trim(peerHead)
		local, err := ensureMergedExists(dir)
		if err != nil {
			return mergedCount, err
		}
		if local == "" {
			// No local auto branch yet: adopt the peer's snapshot as the
			// merge base. Our own snapshot gets merged in on a later run.
			if _, err := run(dir, "update-ref", "refs/heads/auto/merged", peerHead); err != nil {
				return mergedCount, err
			}
			mergedCount++
			continue
		}
		if local == peerHead {
			continue
		}
		tree, err := mergeTree(dir, local, local, peerHead)
		if err != nil {
			return mergedCount, err
		}
		commit, err := run(dir, "commit-tree", tree, "-p", local, "-p", peerHead,
			"-m", fmt.Sprintf("lmicesync merge %s %s", device, peerRef))
		if err != nil {
			return mergedCount, err
		}
		if _, err := run(dir, "update-ref", "refs/heads/auto/merged", trim(commit), local); err != nil {
			return mergedCount, err
		}
		mergedCount++
	}
	return mergedCount, nil
}

// localDevice returns the device name used for the local auto branch. The
// sync command does not know the config device here; callers pass it in via
// the package-level setter below.
var localDeviceName string

func localDevice() string {
	if localDeviceName != "" {
		return localDeviceName
	}
	return "self"
}

// SyncResult describes one repository's outcome.
type SyncResult struct {
	Repo      string   `json:"repo"`
	Merged    int      `json:"merged_peers"`
	Conflicts []string `json:"conflicts,omitempty"`
}

// Run implements `lmicesync sync [--host H]`.
func Run(cfg *config.Config, args []string) error {
	if cfg.Token == "" {
		return fmt.Errorf("sync requires a token; set LMICESYNC_TOKEN or add token to the config")
	}
	localDeviceName = cfg.Device
	host := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--host":
			if i+1 < len(args) {
				host = args[i+1]
				i++
			}
		default:
			return fmt.Errorf("unknown option: %s", args[i])
		}
	}

	var peers []serve.Peer
	if host != "" {
		port := 7370
		h := host
		if idx := strings.LastIndex(host, ":"); idx > 0 {
			fmt.Sscanf(host[idx+1:], "%d", &port)
			h = host[:idx]
		}
		peers = []serve.Peer{{Device: "peer-" + h, HTTPHost: h, HTTPPort: port}}
	} else {
		var err error
		peers, err = serve.Discover(cfg, 3*time.Second)
		if err != nil {
			return err
		}
		if len(peers) == 0 {
			fmt.Println("no peers discovered; try --host <ip> or check the agent is running")
			return nil
		}
	}

	repos := scan.FindRepos(cfg.Roots)
	byName := map[string]string{}
	for _, r := range repos {
		byName[filepath.Base(r)] = r
	}

	// Fetch before merge so a merge runs against fresh refs.
	for _, p := range peers {
		for name, dir := range byName {
			url := fmt.Sprintf("http://%s:%d/v1/sync/%s?device=%s", p.HTTPHost, p.HTTPPort, name, cfg.Device)
			if err := fetchBundle(dir, url, p.Device, cfg.Token); err != nil {
				fmt.Printf("fetch %s from %s: %v\n", name, p.Device, err)
			}
		}
	}

	var results []SyncResult
	for name, dir := range byName {
		res := SyncResult{Repo: name}
		for _, p := range peers {
			n, err := mergePeer(dir, p.Device)
			if err != nil {
				res.Conflicts = append(res.Conflicts,
					fmt.Sprintf("%s: %s", p.Device, err))
				continue
			}
			res.Merged += n
		}
		if res.Merged > 0 || len(res.Conflicts) > 0 {
			results = append(results, res)
		}
	}
	for _, r := range results {
		if len(r.Conflicts) > 0 {
			fmt.Printf("CONFLICT %s: %s\n", r.Repo, strings.Join(r.Conflicts, "; "))
		} else {
			fmt.Printf("merged %s: %d peer(s)\n", r.Repo, r.Merged)
		}
	}
	return nil
}
