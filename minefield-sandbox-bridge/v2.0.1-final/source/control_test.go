package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultControlConfigInternalContinuation(t *testing.T) {
	cfg := defaultControlConfig()
	if !cfg.NeverUseExternalTasks {
		t.Fatal("external task planning must remain disabled")
	}
	if !cfg.Continuation.Enabled || !cfg.Continuation.InternalOnly {
		t.Fatal("internal continuation must be enabled and internal-only")
	}
	if cfg.Continuation.Until == "" || cfg.Continuation.ConversationURLPrefix == "" {
		t.Fatal("continuation cutoff and exact conversation are required")
	}
}

func TestPathAllowedBoundedRoots(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "a", "b.txt")
	if !pathAllowed(child, []string{root}) {
		t.Fatal("child should be allowed")
	}
	outside := filepath.Join(filepath.Dir(root), "outside.txt")
	if pathAllowed(outside, []string{root}) {
		t.Fatal("outside path should be denied")
	}
}

func TestChromeArgumentPreservation(t *testing.T) {
	process := map[string]any{
		"CommandLine": `"C:\\Chrome SxS\\chrome.exe" --user-data-dir="E:\\Profiles\\Minefield" --profile-directory="Profile 1" --load-extension="E:\\Extension" https://chatgpt.com/`,
	}
	args := extractChromePreservedArgs(process)
	if len(args) < 3 {
		t.Fatalf("expected preserved arguments, got %#v", args)
	}
	merged := mergeUniqueArgs([]string{"--restore-last-session", "--remote-debugging-port=9222"}, args)
	count := 0
	for _, arg := range merged {
		if len(arg) >= len("--remote-debugging-port=") && arg[:len("--remote-debugging-port=")] == "--remote-debugging-port=" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("remote debugging argument should be unique, got %#v", merged)
	}
}

func TestRenderBackgroundInjectsToken(t *testing.T) {
	local := t.TempDir()
	user := t.TempDir()
	t.Setenv("LOCALAPPDATA", local)
	t.Setenv("USERPROFILE", user)
	rendered := string(renderBackgroundWithToken([]byte("token=__MF_BROKER_TOKEN__")))
	if rendered == "token=__MF_BROKER_TOKEN__" {
		t.Fatal("token placeholder was not replaced")
	}
	if _, err := os.Stat(filepath.Join(local, "DoubleTab", "MinefieldArtifactMesh", "capabilities.json")); err != nil {
		t.Fatalf("config was not written: %v", err)
	}
}
