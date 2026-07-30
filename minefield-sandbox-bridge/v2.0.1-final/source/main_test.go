package main

import (
	"archive/zip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPatchExtensionPreservesManifestAndIsIdempotent(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	root := t.TempDir()
	manifest := `{
  "manifest_version": 3,
  "name": "Minefield Control (DT Return Origin)",
  "version": "1.9.5",
  "version_name": "1.9.5 stable",
  "key": "durable-test-key",
  "permissions": ["storage", "tabs"],
  "host_permissions": ["https://chatgpt.com/*"],
  "background": {"service_worker": "background.js"},
  "content_scripts": [{
    "matches": ["https://chatgpt.com/*"],
    "exclude_matches": ["https://chatgpt.com/auth/*"],
    "js": ["common.js", "content.js"],
    "run_at": "document_idle"
  }],
  "x-preserve": {"nested": true}
}`
	mustWrite(t, filepath.Join(root, "manifest.json"), manifest)
	mustWrite(t, filepath.Join(root, "background.js"), "async function mfRunWatchdogInvestigator(reason='scheduled') { return reason; }\n")
	mustWrite(t, filepath.Join(root, "common.js"), "globalThis.commonLoaded = true;\n")
	mustWrite(t, filepath.Join(root, "content.js"), "new MutationObserver(()=>{}).observe(document,{subtree:true,childList:true});\n")

	first, err := patchExtension(root)
	if err != nil {
		t.Fatal(err)
	}
	if first.OriginalVersion != "1.9.5" || first.PatchedVersion != version {
		t.Fatalf("unexpected versions: %#v", first)
	}
	if _, err := os.Stat(first.BackupPath); err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	if len(first.GuardedContentFiles) != 2 {
		t.Fatalf("expected 2 guarded files, got %v", first.GuardedContentFiles)
	}

	data, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["version"] != version {
		t.Fatalf("version not patched: %v", doc["version"])
	}
	if _, ok := doc["x-preserve"]; !ok {
		t.Fatal("unknown top-level key lost")
	}
	entries := doc["content_scripts"].([]any)
	if len(entries) != 2 {
		t.Fatalf("expected original + selfrepair content entry, got %d", len(entries))
	}
	original := entries[0].(map[string]any)
	if _, ok := original["exclude_matches"]; !ok {
		t.Fatal("unknown content-script key lost")
	}
	if !containsAnyString(doc["permissions"], "debugger") {
		t.Fatal("debugger permission missing")
	}
	if !containsAnyString(doc["host_permissions"], "http://127.0.0.1/*") {
		t.Fatal("loopback host permission missing")
	}

	bg := mustRead(t, filepath.Join(root, "background.js"))
	if !strings.Contains(bg, patchMarker) || !strings.Contains(bg, "mf_contract_core_v200.js") || !strings.Contains(bg, "mf_self_repair_background_v200.js") {
		t.Fatal("background import marker/core missing")
	}
	if _, err := os.Stat(filepath.Join(root, "mf_contract_core_v200.js")); err != nil {
		t.Fatalf("contract core missing: %v", err)
	}
	for _, file := range []string{"common.js", "content.js"} {
		text := mustRead(t, filepath.Join(root, file))
		if strings.Count(text, legacyGuardMark) != 1 {
			t.Fatalf("guard missing or duplicated in %s", file)
		}
	}

	second, err := patchExtension(root)
	if err != nil {
		t.Fatal(err)
	}
	if !second.AlreadyPatched {
		t.Fatal("second patch should report already patched")
	}
	for _, file := range []string{"common.js", "content.js"} {
		text := mustRead(t, filepath.Join(root, file))
		if strings.Count(text, legacyGuardMark) != 1 {
			t.Fatalf("second patch duplicated guard in %s", file)
		}
	}
	bg = mustRead(t, filepath.Join(root, "background.js"))
	if strings.Count(bg, patchMarker) != 1 {
		t.Fatal("second patch duplicated background import")
	}
}

func TestBackupZipContainsOriginal(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "manifest.json"), `{"manifest_version":3,"name":"Minefield Control","version":"1.9.5","background":{"service_worker":"background.js"},"content_scripts":[]}`)
	mustWrite(t, filepath.Join(root, "background.js"), "// original background\n")
	result, err := patchExtension(root)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.OpenReader(result.BackupPath)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	found := false
	for _, f := range zr.File {
		if f.Name == "background.js" {
			found = true
		}
	}
	if !found {
		t.Fatal("backup zip omitted original background.js")
	}
}

func TestExtractRepairEnvelope(t *testing.T) {
	text := `noise [[MINEFIELD_REPAIR/1]] {"state":"NEEDS_REPAIR","actions":[{"action":"no-op"}]} [[/MINEFIELD_REPAIR]] trailing`
	out := extractRepairEnvelope(text)
	if out == nil || out["state"] != "NEEDS_REPAIR" {
		t.Fatalf("failed to parse: %#v", out)
	}
}

func mustWrite(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}
func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
func containsAnyString(value any, wanted string) bool {
	for _, v := range value.([]any) {
		if v == wanted {
			return true
		}
	}
	return false
}

func TestIncidentEndpointRequiresBrokerToken(t *testing.T) {
	cp := newTestControlPlane(t)
	b := newBroker(cp.root)
	b.control = cp
	req := httptest.NewRequest(http.MethodPost, "/incident", strings.NewReader(`{"schema":"minefield.incident/1"}`))
	res := httptest.NewRecorder()
	b.handleIncident(res, req)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", res.Code, res.Body.String())
	}
}
