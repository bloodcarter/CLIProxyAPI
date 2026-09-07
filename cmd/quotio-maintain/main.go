// Local macOS installation gate for the maintained Quotio engine.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"gopkg.in/yaml.v3"
)

type buildRecord struct {
	Version string
	Commit  string
	SHA256  string
}

func main() {
	binary := flag.String("binary", "", "Built candidate executable")
	version := flag.String("version", "", "Distinct version directory")
	commit := flag.String("commit", "", "Source commit")
	check := flag.Bool("check", false, "Verify installed hash and running executable only")
	flag.Parse()
	if err := run(*binary, *version, *commit, *check); err != nil {
		fmt.Fprintln(os.Stderr, "MAINTENANCE_FAILED:", err)
		os.Exit(1)
	}
}

func run(binary, version, commit string, check bool) error {
	if !regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+-quotiofix\.[0-9a-f]{12}$`).MatchString(version) || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(commit) {
		return errors.New("invalid release identity")
	}
	userDir, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	root := filepath.Join(userDir, "Library/Application Support/Quotio")
	engines := filepath.Join(root, "proxy/upstream")
	current := filepath.Join(engines, "current")
	target := filepath.Join(engines, version)
	configBytes, err := os.ReadFile(filepath.Join(root, "config.yaml"))
	if err != nil {
		return err
	}
	var cfg map[string]any
	if err = yaml.Unmarshal(configBytes, &cfg); err != nil {
		return err
	}
	port, ok := cfg["port"].(int)
	if !ok || port < 1 || port > 65535 {
		return errors.New("invalid proxy port")
	}
	keys, ok := cfg["api-keys"].([]any)
	if !ok || len(keys) == 0 {
		return errors.New("no configured local API key")
	}
	key, ok := keys[0].(string)
	if !ok || key == "" {
		return errors.New("invalid local API key")
	}
	if check {
		if err = verifyBuild(target, version, commit); err != nil {
			return err
		}
		pid, err := listenerPID(port)
		if err != nil {
			return err
		}
		if !runningFrom(pid, target) {
			return errors.New("running engine differs from maintained installation")
		}
		fmt.Println("INSTALLATION_VERIFIED", version)
		return nil
	}
	if binary == "" {
		return errors.New("candidate binary required")
	}
	pid, err := listenerPID(port)
	if err != nil {
		return fmt.Errorf("Quotio must be running before promotion: %w", err)
	}
	previous, err := os.Readlink(current)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(previous) {
		previous = filepath.Join(engines, previous)
	}
	previous = filepath.Clean(previous)
	if !strings.HasPrefix(previous, engines+string(os.PathSeparator)) || !runningFrom(pid, previous) {
		return errors.New("listener is not the selected Quotio engine")
	}
	scratch, err := os.MkdirTemp("", "quotio-candidate-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	// Only the temporary candidate receives altered configuration.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	probePort := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	cfg["host"] = "127.0.0.1"
	cfg["port"] = probePort
	cfg["debug"] = false
	cfg["request-log"] = false
	cfg["logging-to-file"] = false
	cfg["usage-statistics-enabled"] = false
	encoded, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	probeConfig := filepath.Join(scratch, "config.yaml")
	if err = os.WriteFile(probeConfig, encoded, 0600); err != nil {
		return err
	}
	logFile, err := os.OpenFile(filepath.Join(scratch, "candidate.log"), os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	candidate := exec.Command(binary, "-config", probeConfig)
	candidate.Dir = scratch
	candidate.Stdout = logFile
	candidate.Stderr = logFile
	if err = candidate.Start(); err != nil {
		return err
	}
	defer stopCandidate(candidate)
	if err = waitListening(probePort, 30*time.Second); err != nil {
		return err
	}
	probePID, err := listenerPID(probePort)
	if err != nil || probePID != candidate.Process.Pid {
		return errors.New("candidate port is not owned by the candidate process")
	}
	if err = smoke(probePort, key); err != nil {
		return fmt.Errorf("candidate rejected before promotion: %w", err)
	}
	fmt.Println("CANDIDATE_SMOKE_OK", version)
	data, err := os.ReadFile(binary)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(data)
	record := buildRecord{version, commit, hex.EncodeToString(digest[:])}
	if _, err = os.Stat(target); err == nil {
		if err = verifyBuild(target, version, commit); err != nil {
			return err
		}
		existing, err := os.ReadFile(filepath.Join(target, "CLIProxyAPI"))
		if err != nil {
			return err
		}
		if sha256.Sum256(existing) != digest {
			return errors.New("existing version has different build bytes")
		}
	} else if !os.IsNotExist(err) {
		return err
	} else {
		stage, err := os.MkdirTemp(engines, ".install-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(stage)
		if err = os.WriteFile(filepath.Join(stage, "CLIProxyAPI"), data, 0755); err != nil {
			return err
		}
		metadata, _ := json.MarshalIndent(record, "", "  ")
		if err = os.WriteFile(filepath.Join(stage, "BUILD.json"), metadata, 0644); err != nil {
			return err
		}
		if err = os.Rename(stage, target); err != nil {
			return err
		}
	}
	if err = activate(current, target); err != nil {
		return err
	}
	// Quotio owns the process and restarts unexpected exits using current/CLIProxyAPI.
	promotionErr := restartAndVerify(pid, port, target, key)
	if promotionErr != nil {
		restoreErr := activate(current, previous)
		if restoreErr == nil {
			if rollbackPID, e := listenerPID(port); e == nil {
				restoreErr = restartAndVerify(rollbackPID, port, previous, key)
			} else {
				restoreErr = waitAndSmoke(port, previous, key)
			}
		}
		return fmt.Errorf("promotion failed: %v; rollback result: %v", promotionErr, restoreErr)
	}
	fmt.Println("INSTALLED_AND_VERIFIED", version, record.SHA256)
	return nil
}

func stopCandidate(candidate *exec.Cmd) {
	_ = candidate.Process.Signal(syscall.SIGTERM)
	done := make(chan struct{})
	go func() { _ = candidate.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = candidate.Process.Kill()
		<-done
	}
}

func verifyBuild(dir, version, commit string) error {
	raw, err := os.ReadFile(filepath.Join(dir, "BUILD.json"))
	if err != nil {
		return err
	}
	var r buildRecord
	if err = json.Unmarshal(raw, &r); err != nil {
		return err
	}
	b, err := os.ReadFile(filepath.Join(dir, "CLIProxyAPI"))
	if err != nil {
		return err
	}
	sum := sha256.Sum256(b)
	if r.Version != version || r.Commit != commit || r.SHA256 != hex.EncodeToString(sum[:]) {
		return errors.New("installed build identity or hash mismatch")
	}
	return nil
}

func activate(current, target string) error {
	temp := current + ".promote-" + strconv.Itoa(os.Getpid())
	if err := os.Symlink(target, temp); err != nil {
		return err
	}
	defer os.Remove(temp)
	return os.Rename(temp, current)
}

func listenerPID(port int) (int, error) {
	b, err := exec.Command("/usr/sbin/lsof", "-nP", "-t", fmt.Sprintf("-iTCP:%d", port), "-sTCP:LISTEN").Output()
	if err != nil {
		return 0, errors.New("no local proxy listener")
	}
	ids := strings.Fields(string(b))
	if len(ids) != 1 {
		return 0, errors.New("ambiguous proxy listener")
	}
	return strconv.Atoi(ids[0])
}

func runningFrom(pid int, dir string) bool {
	b, err := exec.Command("/usr/sbin/lsof", "-a", "-p", strconv.Itoa(pid), "-d", "txt", "-Fn").Output()
	if err != nil {
		return false
	}
	want := "n" + filepath.Join(dir, "CLIProxyAPI")
	for _, line := range strings.Split(string(b), "\n") {
		if line == want {
			return true
		}
	}
	return false
}

func waitListening(port int, d time.Duration) error {
	until := time.Now().Add(d)
	for time.Now().Before(until) {
		c, e := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
		if e == nil {
			c.Close()
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return errors.New("proxy listener did not start")
}

func restartAndVerify(pid, port int, dir, key string) error {
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return err
	}
	return waitAndSmoke(port, dir, key)
}

func waitAndSmoke(port int, dir, key string) error {
	until := time.Now().Add(45 * time.Second)
	for time.Now().Before(until) {
		pid, e := listenerPID(port)
		if e == nil && runningFrom(pid, dir) {
			return smoke(port, key)
		}
		time.Sleep(500 * time.Millisecond)
	}
	return errors.New("Quotio did not start the selected engine")
}

func smoke(port int, key string) error {
	h := http.Header{"Authorization": []string{"Bearer " + key}, "Session_id": []string{fmt.Sprintf("quotio-maintenance-%d", time.Now().UnixNano())}}
	d := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 70*time.Second)
	defer cancel()
	c, _, err := d.DialContext(ctx, fmt.Sprintf("ws://127.0.0.1:%d/v1/responses", port), h)
	if err != nil {
		return fmt.Errorf("WebSocket handshake: %w", err)
	}
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.SetWriteDeadline(time.Now().Add(10 * time.Second))
	const marker = "QUOTIO_NAMED_INPUT_OK"
	input := map[string]any{"type": "function_call_output", "namespace": "codex_app", "name": "automation_update", "output": "Call maintenance_probe exactly once with marker " + marker + "."}
	tool := map[string]any{"type": "function", "name": "maintenance_probe", "description": "Diagnostic marker only, no external action.", "parameters": map[string]any{"type": "object", "properties": map[string]any{"marker": map[string]any{"type": "string"}}, "required": []string{"marker"}, "additionalProperties": false}, "strict": true}
	req := map[string]any{"type": "response.create", "model": "gpt-6-astra", "instructions": "Perform the task supplied as a named standalone tool output. If no input task exists, say NO_TASK.", "input": []any{input}, "tools": []any{tool}, "store": false, "reasoning": map[string]any{"effort": "low"}}
	if err = c.WriteJSON(req); err != nil {
		return err
	}
	called := false
	for {
		var e struct {
			Type   string                                 `json:"type"`
			Item   struct{ Type, Name, Arguments string } `json:"item"`
			Status int                                    `json:"status"`
			Error  struct {
				Code    string `json:"code"`
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err = c.ReadJSON(&e); err != nil {
			return fmt.Errorf("diagnostic response incomplete: %w", err)
		}
		if e.Type == "response.output_item.done" && e.Item.Type == "function_call" && e.Item.Name == "maintenance_probe" {
			var args struct{ Marker string }
			if json.Unmarshal([]byte(e.Item.Arguments), &args) == nil && args.Marker == marker {
				called = true
			}
		}
		if e.Type == "error" || e.Type == "response.failed" {
			return fmt.Errorf("upstream diagnostic error: status=%d code=%s type=%s message=%.500s", e.Status, e.Error.Code, e.Error.Type, e.Error.Message)
		}
		if e.Type == "response.completed" {
			if !called {
				return errors.New("named task did not produce the required tool call")
			}
			return nil
		}
	}
}
