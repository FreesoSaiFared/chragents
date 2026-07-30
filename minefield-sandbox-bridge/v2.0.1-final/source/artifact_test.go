package main

import (
	"archive/zip"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestControlPlane(t *testing.T) *ControlPlane {
	t.Helper()
	local := t.TempDir()
	user := t.TempDir()
	t.Setenv("LOCALAPPDATA", local)
	t.Setenv("USERPROFILE", user)
	t.Setenv("MF_TEST_MODE", "1")
	cp, err := newControlPlane(local)
	if err != nil {
		t.Fatal(err)
	}
	return cp
}

func writeZip(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, text := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(text)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestArtifactExtractDeleteAndReceipt(t *testing.T) {
	cp := newTestControlPlane(t)
	downloads := t.TempDir()
	path := filepath.Join(downloads, "demo.zip")
	writeZip(t, path, map[string]string{"a.txt": "hello", "nested/b.txt": "world"})
	policy := defaultControlConfig().Artifacts
	policy.DownloadsRoot = downloads
	policy.QuarantineRoot = filepath.Join(downloads, "_q")
	receipt := cp.processZipArtifact(path, "https://chatgpt.com/g/g-p-test/c/conversation", policy)
	if receipt.Status != "EXTRACTED" {
		t.Fatalf("unexpected status: %#v", receipt)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("zip should be deleted, err=%v", err)
	}
	if got := mustRead(t, filepath.Join(downloads, "demo", "a.txt")); got != "hello" {
		t.Fatalf("bad extraction: %q", got)
	}
	if _, err := os.Stat(filepath.Join(downloads, "demo", ".minefield-artifact-receipt.json")); err != nil {
		t.Fatalf("receipt missing: %v", err)
	}
}

func TestArtifactRejectsNumberedDuplicate(t *testing.T) {
	cp := newTestControlPlane(t)
	downloads := t.TempDir()
	path := filepath.Join(downloads, "demo(1).zip")
	writeZip(t, path, map[string]string{"a.txt": "hello"})
	policy := defaultControlConfig().Artifacts
	policy.DownloadsRoot = downloads
	receipt := cp.processZipArtifact(path, "https://chatgpt.com/g/g-p-test/c/conversation", policy)
	if receipt.Status != "OUT_OF_SYNC_DUPLICATE_FILENAME" {
		t.Fatalf("unexpected status: %#v", receipt)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("duplicate zip should remain for evidence: %v", err)
	}
}

func TestArtifactRejectsExistingTarget(t *testing.T) {
	cp := newTestControlPlane(t)
	downloads := t.TempDir()
	path := filepath.Join(downloads, "demo.zip")
	writeZip(t, path, map[string]string{"a.txt": "hello"})
	if err := os.Mkdir(filepath.Join(downloads, "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	policy := defaultControlConfig().Artifacts
	policy.DownloadsRoot = downloads
	receipt := cp.processZipArtifact(path, "https://chatgpt.com/g/g-p-test/c/conversation", policy)
	if receipt.Status != "OUT_OF_SYNC_TARGET_EXISTS" {
		t.Fatalf("unexpected status: %#v", receipt)
	}
}

func TestArtifactRejectsTraversal(t *testing.T) {
	cp := newTestControlPlane(t)
	downloads := t.TempDir()
	path := filepath.Join(downloads, "evil.zip")
	writeZip(t, path, map[string]string{"../escape.txt": "no"})
	policy := defaultControlConfig().Artifacts
	policy.DownloadsRoot = downloads
	receipt := cp.processZipArtifact(path, "https://chatgpt.com/g/g-p-test/c/conversation", policy)
	if receipt.Status != "ZIP_VALIDATION_FAILED" || !strings.Contains(strings.ToLower(receipt.Error), "traversal") {
		t.Fatalf("unexpected status: %#v", receipt)
	}
}

func TestReturnSpoolAckIsDurable(t *testing.T) {
	cp := newTestControlPlane(t)
	env := ReturnEnvelope{
		Schema: "minefield.return/1", ID: "return-test-123", Kind: "artifact.result",
		OriginURL:       "https://chatgpt.com/g/g-p-test/c/conversation",
		ConversationKey: "chat://chatgpt/g-p-test/conversation",
		Payload:         map[string]any{"ok": true},
	}
	if err := cp.emitReturn(env); err != nil {
		t.Fatal(err)
	}
	pending := filepath.Join(cp.returnPendingRoot(), "return-test-123.json")
	if _, err := os.Stat(pending); err != nil {
		t.Fatalf("pending return missing: %v", err)
	}

	body := strings.NewReader(`{"id":"return-test-123","outcome":"SUBMITTED_EXACT_ORIGIN","evidence":{"tabId":7}}`)
	req := httptest.NewRequest("POST", "/return/ack", body)
	req.Header.Set("X-Minefield-Token", cp.token())
	res := httptest.NewRecorder()
	cp.handleReturnAck(res, req)
	if res.Code != 200 {
		t.Fatalf("ack failed: %d %s", res.Code, res.Body.String())
	}
	if _, err := os.Stat(pending); !os.IsNotExist(err) {
		t.Fatalf("pending return should be removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cp.returnAckRoot(), "return-test-123.json")); err != nil {
		t.Fatalf("acked return missing: %v", err)
	}
}

func TestArtifactReturnIsReadyOnlyAfterCommit(t *testing.T) {
	cp := newTestControlPlane(t)
	downloads := t.TempDir()
	path := filepath.Join(downloads, "commit.zip")
	writeZip(t, path, map[string]string{"a.txt": "hello"})
	policy := defaultControlConfig().Artifacts
	policy.DownloadsRoot = downloads
	receipt := cp.processZipArtifact(path, "https://chatgpt.com/g/g-p-test/c/conversation", policy)
	if receipt.Status != "EXTRACTED" {
		t.Fatalf("unexpected receipt: %#v", receipt)
	}
	data, err := os.ReadFile(filepath.Join(cp.returnPendingRoot(), "return-"+receipt.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var env ReturnEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatal(err)
	}
	if env.State != "READY" {
		t.Fatalf("return should be READY after ZIP deletion, got %q", env.State)
	}
}

func TestRecoverCommittingReturnAfterCrashBoundary(t *testing.T) {
	cp := newTestControlPlane(t)
	downloads := t.TempDir()
	target := filepath.Join(downloads, "recovered")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, ".minefield-artifact-receipt.json"), []byte(`{"ok":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	receipt := ArtifactReceipt{
		Schema: artifactSchema, ID: "artifact-crash", Status: "EXTRACTED",
		ArchivePath: filepath.Join(downloads, "recovered.zip"), ArchiveName: "recovered.zip",
		TargetDirectory: target, Origin: ArtifactOrigin{OriginURL: "https://chatgpt.com/g/g-p-test/c/conversation"},
		Evidence: map[string]any{},
	}
	if err := cp.persistArtifactReturn(receipt, "COMMITTING"); err != nil {
		t.Fatal(err)
	}
	cp.recoverCommittingReturns()
	data, err := os.ReadFile(filepath.Join(cp.returnPendingRoot(), "return-artifact-crash.json"))
	if err != nil {
		t.Fatal(err)
	}
	var env ReturnEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatal(err)
	}
	if env.State != "READY" {
		t.Fatalf("recovered return should be READY, got %q", env.State)
	}
}
