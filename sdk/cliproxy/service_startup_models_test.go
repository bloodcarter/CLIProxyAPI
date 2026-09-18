package cliproxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	internalregistry "github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestServiceRunRegistersLoadedCodexModelsBeforeListenerStart(t *testing.T) {
	const (
		authID = "startup-codex-auth"
		model  = "gpt-6-astra"
	)

	modelRegistry := internalregistry.GetGlobalRegistry()
	modelRegistry.UnregisterClient(authID)
	t.Cleanup(func() { modelRegistry.UnregisterClient(authID) })

	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{
		ID:       authID,
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"plan_type": "pro",
		},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	dir := t.TempDir()
	occupied, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("reserve listener: %v", errListen)
	}
	defer occupied.Close()
	port := occupied.Addr().(*net.TCPAddr).Port
	configPath := filepath.Join(dir, "config.yaml")
	if errWrite := os.WriteFile(configPath, []byte(fmt.Sprintf("host: 127.0.0.1\nport: %d\n", port)), 0o600); errWrite != nil {
		t.Fatalf("write config: %v", errWrite)
	}

	var beforeStartErr error
	service, errBuild := NewBuilder().
		WithConfig(&config.Config{Host: "127.0.0.1", Port: port, AuthDir: dir}).
		WithConfigPath(configPath).
		WithCoreAuthManager(manager).
		WithHooks(Hooks{OnBeforeStart: func(*config.Config) {
			models := modelRegistry.GetModelsForClient(authID)
			found := false
			for _, info := range models {
				if info != nil && info.ID == model {
					found = true
					break
				}
			}
			if !found {
				beforeStartErr = errors.New("gpt-6-astra is not registered before listener start")
				return
			}

			manager.RegisterExecutor(&syncTestExecutor{})
			response, errExecute := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
			if errExecute != nil || string(response.Payload) != authID {
				beforeStartErr = errors.New("gpt-6-astra auth is not scheduler-selectable before listener start")
			}
		}}).
		Build()
	if errBuild != nil {
		t.Fatalf("Build() failed: %v", errBuild)
	}

	errRun := service.Run(context.Background())
	if errRun == nil || !strings.Contains(errRun.Error(), "address already in use") {
		t.Fatalf("Run() error = %v, want occupied-listener failure after the pre-start assertion", errRun)
	}
	if beforeStartErr != nil {
		t.Fatal(beforeStartErr)
	}
}
