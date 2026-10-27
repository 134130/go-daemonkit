package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLaunchEnvironmentHelper(t *testing.T) {
	path := os.Getenv("DAEMONKIT_ENV_RESULT")
	if path == "" {
		return
	}
	_, unwanted := os.LookupEnv("DAEMONKIT_UNWANTED")
	_, empty := os.LookupEnv("DAEMONKIT_EMPTY")
	if unwanted || !empty || os.Getenv("DAEMONKIT_EMPTY") != "" || os.Getenv(launchNonceEnv) == "untrusted" {
		t.Fatal("invalid environment")
	}
	if err := os.WriteFile(path, []byte(os.Getenv("DAEMONKIT_VALUE")), 0600); err != nil {
		t.Fatal(err)
	}
	TestManagerDaemonHelper(t)
}

func TestLaunchEnvironmentSurvivesUpdateRollback(t *testing.T) {
	original, _ := testManager(t)
	t.Setenv("DAEMONKIT_UNWANTED", "caller-secret")
	t.Setenv("DAEMONKIT_VALUE", "caller-B")
	path := filepath.Join(t.TempDir(), "environment")
	env := []string{}
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "DAEMONKIT_UNWANTED=") {
			env = append(env, entry)
		}
	}
	env = append(env, "DAEMONKIT_ENV_RESULT="+path, "DAEMONKIT_EMPTY=", "DAEMONKIT_VALUE=daemon-A", launchNonceEnv+"=untrusted")
	m, err := NewManagerWithLaunch(original.app.options, LaunchOptions{Env: env}, "-test.run=^TestLaunchEnvironmentHelper$")
	if err != nil {
		t.Fatal(err)
	}
	// Caller's slice is not retained.
	env[len(env)-2] = "DAEMONKIT_VALUE=mutated"
	if err := m.EnsureRunning(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := m.Restart(t.Context()); err != nil {
		t.Fatal(err)
	}
	next := updateCandidate(t, m)
	next.args = []string{"-test.run=^TestUpdateExitHelper$"}
	if err := next.Update(t.Context(), next.executable); !errors.Is(err, ErrUpdateRolledBack) {
		t.Fatalf("rollback: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "daemon-A" {
		t.Fatalf("child env: %q, %v", data, err)
	}
	if os.Getenv("DAEMONKIT_VALUE") != "caller-B" {
		t.Fatal("parent environment changed")
	}
	for _, name := range []string{"current.json", "runtime.json"} {
		data, err := os.ReadFile(filepath.Join(m.app.directory, name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "daemon-A") || strings.Contains(string(data), "caller-secret") {
			t.Fatal("environment persisted")
		}
	}
	if err := m.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestLaunchEmptyEnvironment(t *testing.T) {
	app := testApp(t)
	m, err := NewManagerWithLaunch(app.options, LaunchOptions{Env: []string{}}, "daemon")
	if err != nil {
		t.Fatal(err)
	}
	if m.env == nil {
		t.Fatal("empty environment became inherited")
	}
}
