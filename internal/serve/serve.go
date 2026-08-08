// Package serve implements the LAN agent: a UDP discovery beacon and a small
// HTTP server that serves git bundles of auto/* refs to approved, token
// authenticated peers. No public network traffic is ever generated.
package serve

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mmdmcy/lmicesync/internal/config"
	"github.com/mmdmcy/lmicesync/internal/scan"
)

const defaultBeaconPort = 10001

// Peer describes a discovered device.
type Peer struct {
	Device     string `json:"device"`
	HTTPHost   string `json:"http_host"`
	HTTPPort   int    `json:"http_port"`
	Discovered string `json:"discovered,omitempty"`
}

type beaconMsg struct {
	Device   string `json:"device"`
	HTTPPort int    `json:"http_port"`
}

func ownAddr() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "127.0.0.1"
	}
	defer conn.Close()
	return strings.Split(conn.LocalAddr().String(), ":")[0]
}

// httpPortFor extracts the configured HTTP port from http_addr.
func httpPortFor(cfg *config.Config) int {
	addr := cfg.HTTPAddr
	port := 7370
	if idx := strings.LastIndex(addr, ":"); idx >= 0 {
		fmt.Sscanf(addr[idx+1:], "%d", &port)
	}
	return port
}

// Run starts the agent: beacon responder plus HTTP server. Blocks.
func Run(cfg *config.Config) error {
	if cfg.Token == "" {
		return fmt.Errorf("serve requires a token; set LMICESYNC_TOKEN or add token to the config")
	}
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return err
	}
	repos := scan.FindRepos(cfg.Roots)
	byName := map[string]string{}
	for _, r := range repos {
		byName[filepath.Base(r)] = r
	}

	httpAddr := cfg.HTTPAddr
	if strings.HasPrefix(httpAddr, ":") {
		httpAddr = ownAddr() + httpAddr
	}
	port := httpPortFor(cfg)

	go beaconResponder(cfg, port)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("/v1/sync/", func(w http.ResponseWriter, req *http.Request) {
		if !authorized(req, cfg) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !peerApproved(req.URL.Query().Get("device"), cfg) {
			http.Error(w, "device not approved", http.StatusForbidden)
			return
		}
		name := strings.TrimPrefix(req.URL.Path, "/v1/sync/")
		dir, ok := byName[name]
		if !ok {
			http.Error(w, "unknown repository", http.StatusNotFound)
			return
		}
		bundleRepo(w, req, dir)
	})
	fmt.Printf("lmicesync agent on http://%s:%d (device %s)\n", ownAddr(), port, cfg.Device)
	return http.ListenAndServe(fmt.Sprintf(":%d", port), mux)
}

func authorized(r *http.Request, cfg *config.Config) bool {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	return subtle.ConstantTimeCompare([]byte(tok), []byte(cfg.Token)) == 1
}

// peerApproved reports whether the requesting device may pull bundles. With
// require_approval off, every device is approved.
func peerApproved(device string, cfg *config.Config) bool {
	if !cfg.RequireApproval {
		return true
	}
	if device == "" {
		return false
	}
	data, err := os.ReadFile(cfg.ApprovalFile)
	if err != nil {
		return false
	}
	var list []string
	if json.Unmarshal(data, &list) != nil {
		return false
	}
	for _, d := range list {
		if d == device {
			return true
		}
	}
	return false
}

// bundleRepo streams a git bundle containing all auto/* refs of the repo.
func bundleRepo(w http.ResponseWriter, req *http.Request, dir string) {
	bundle, err := os.CreateTemp("", "lmicesync-bundle-*")
	if err != nil {
		http.Error(w, "bundle failed", http.StatusInternalServerError)
		return
	}
	defer os.Remove(bundle.Name())
	defer bundle.Close()

	refsOut, err := exec.Command("git", "-C", dir, "for-each-ref",
		"--format=%(refname)", "refs/heads/auto/").Output()
	if err != nil || strings.TrimSpace(string(refsOut)) == "" {
		http.Error(w, "no auto refs", http.StatusNotFound)
		return
	}
	refs := strings.Fields(string(refsOut))
	args := append([]string{"-C", dir, "bundle", "create", bundle.Name()}, refs...)
	cmd := exec.Command("git", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		http.Error(w, fmt.Sprintf("bundle failed: %v: %s", err, out), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/x-git-bundle")
	http.ServeFile(w, req, bundle.Name())
}

func beaconResponder(cfg *config.Config, httpPort int) {
	conn, err := net.ListenPacket("udp", fmt.Sprintf(":%d", cfg.BeaconPort))
	if err != nil {
		fmt.Printf("beacon listen failed: %v\n", err)
		return
	}
	defer conn.Close()
	buf := make([]byte, 2048)
	for {
		n, addr, err := conn.ReadFrom(buf)
		if err != nil {
			return
		}
		var msg beaconMsg
		if json.Unmarshal(buf[:n], &msg) != nil || msg.Device == "" {
			continue
		}
		if msg.Device == cfg.Device {
			continue
		}
		reply, _ := json.Marshal(beaconMsg{Device: cfg.Device, HTTPPort: httpPort})
		_, _ = conn.WriteTo(reply, addr)
	}
}

// Discover broadcasts a beacon and collects peer replies for the given wait.
func Discover(cfg *config.Config, wait time.Duration) ([]Peer, error) {
	conn, err := net.ListenPacket("udp", ":0")
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	msg, _ := json.Marshal(beaconMsg{Device: cfg.Device, HTTPPort: httpPortFor(cfg)})
	for _, ip := range []net.IP{net.IPv4(255, 255, 255, 255), net.IPv4(127, 0, 0, 1)} {
		if _, err := conn.WriteTo(msg, &net.UDPAddr{IP: ip, Port: cfg.BeaconPort}); err != nil {
			return nil, err
		}
	}
	deadline := time.Now().Add(wait)
	seen := map[string]bool{}
	var peers []Peer
	buf := make([]byte, 2048)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(deadline)
		n, from, err := conn.ReadFrom(buf)
		if err != nil {
			break
		}
		var reply beaconMsg
		if json.Unmarshal(buf[:n], &reply) != nil || reply.Device == "" ||
			reply.Device == cfg.Device {
			continue
		}
		host := strings.Split(from.String(), ":")[0]
		port := reply.HTTPPort
		if port == 0 {
			port = 7370
		}
		key := reply.Device + "@" + host
		if seen[key] {
			continue
		}
		seen[key] = true
		peers = append(peers, Peer{Device: reply.Device, HTTPHost: host, HTTPPort: port})
	}
	sort.Slice(peers, func(i, j int) bool { return peers[i].Device < peers[j].Device })
	return peers, nil
}

// PrintPeers implements `lmicesync peers`.
func PrintPeers(cfg *config.Config) error {
	peers, err := Discover(cfg, 3*time.Second)
	if err != nil {
		return err
	}
	if len(peers) == 0 {
		fmt.Println("no peers discovered")
		return nil
	}
	for _, p := range peers {
		fmt.Printf("%s %s:%d\n", p.Device, p.HTTPHost, p.HTTPPort)
	}
	return nil
}
