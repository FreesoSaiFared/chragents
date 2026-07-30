package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	version         = "2.0.1"
	extensionID     = "inhjgnkfjaehkgjonheafkcpbfejpjjh"
	brokerPort      = 9789
	cdpBase         = "http://127.0.0.1:9222"
	mcpPackage      = "chrome-devtools-mcp@1.6.0"
	patchMarker     = "MINEFIELD_TOTAL_CONTROL_V200"
	legacyGuardMark = "MINEFIELD_LEGACY_CONTENT_GUARD_V200"
)

//go:embed mf_contract_core_v200.js
var contractCoreJS []byte

//go:embed mf_self_repair_background_v200.js
var backgroundJS []byte

//go:embed mf_self_repair_content_v200.js
var contentJS []byte

type Manifest struct {
	Name            string                     `json:"name"`
	Version         string                     `json:"version"`
	VersionName     string                     `json:"version_name,omitempty"`
	ManifestVersion int                        `json:"manifest_version"`
	Permissions     []string                   `json:"permissions,omitempty"`
	HostPermissions []string                   `json:"host_permissions,omitempty"`
	Background      map[string]any             `json:"background,omitempty"`
	ContentScripts  []ContentScript            `json:"content_scripts,omitempty"`
	Other           map[string]json.RawMessage `json:"-"`
}

type ContentScript struct {
	Matches         []string `json:"matches"`
	JS              []string `json:"js,omitempty"`
	CSS             []string `json:"css,omitempty"`
	RunAt           string   `json:"run_at,omitempty"`
	AllFrames       bool     `json:"all_frames,omitempty"`
	MatchAboutBlank bool     `json:"match_about_blank,omitempty"`
	World           string   `json:"world,omitempty"`
}

type PatchResult struct {
	Root                string            `json:"root"`
	ManifestPath        string            `json:"manifestPath"`
	BackupPath          string            `json:"backupPath"`
	OriginalVersion     string            `json:"originalVersion"`
	PatchedVersion      string            `json:"patchedVersion"`
	BackgroundFile      string            `json:"backgroundFile"`
	GuardedContentFiles []string          `json:"guardedContentFiles"`
	WrittenFiles        map[string]string `json:"writtenFilesSha256"`
	AlreadyPatched      bool              `json:"alreadyPatched"`
	Warnings            []string          `json:"warnings,omitempty"`
}

type InstallResult struct {
	Schema          string       `json:"schema"`
	Version         string       `json:"version"`
	Status          string       `json:"status"`
	StartedAt       string       `json:"startedAt"`
	CompletedAt     string       `json:"completedAt"`
	Executable      string       `json:"executable"`
	InstalledBroker string       `json:"installedBroker"`
	Extension       *PatchResult `json:"extension,omitempty"`
	BrokerStarted   bool         `json:"brokerStarted"`
	BrokerHealth    any          `json:"brokerHealth,omitempty"`
	ReloadAttempted bool         `json:"reloadAttempted"`
	ReloadVerified  bool         `json:"reloadVerified"`
	ReloadEvidence  any          `json:"reloadEvidence,omitempty"`
	ResultPaths     []string     `json:"resultPaths"`
	Errors          []string     `json:"errors,omitempty"`
	Warnings        []string     `json:"warnings,omitempty"`
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	cmd := "install"
	if len(os.Args) > 1 {
		cmd = strings.ToLower(os.Args[1])
	}
	var err error
	switch cmd {
	case "install", "run":
		err = install()
	case "broker", "serve":
		err = runBroker()
	case "doctor":
		err = doctor()
	case "elevated-install":
		exe, _ := os.Executable()
		err = installElevatedHelper(exe)
	case "elevated-worker":
		err = runElevatedWorker()
	case "patch":
		if len(os.Args) < 3 {
			err = errors.New("usage: patch <extension-root>")
			break
		}
		var result *PatchResult
		result, err = patchExtension(os.Args[2])
		if result != nil {
			printJSON(result)
		}
	case "rollback":
		if len(os.Args) < 4 {
			err = errors.New("usage: rollback <extension-root> <backup.zip>")
			break
		}
		err = restoreZip(os.Args[3], os.Args[2])
	case "version", "--version", "-v":
		fmt.Println(version)
	default:
		err = fmt.Errorf("unknown command %q", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}
}

func install() error {
	started := time.Now().UTC()
	result := InstallResult{
		Schema:    "minefield.total-control.install-result/1",
		Version:   version,
		Status:    "FAILED",
		StartedAt: started.Format(time.RFC3339Nano),
	}
	exe, _ := os.Executable()
	result.Executable = exe

	if _, cfgErr := ensureControlConfig(); cfgErr != nil {
		result.Errors = append(result.Errors, "control config: "+cfgErr.Error())
	}

	installDir := filepath.Join(localAppData(), "DoubleTab", "MinefieldArtifactMesh")
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		return err
	}
	installedExe := filepath.Join(installDir, "MinefieldArtifactMeshBroker.exe")
	if runtime.GOOS != "windows" {
		installedExe = filepath.Join(installDir, "MinefieldArtifactMesh")
	}
	if !samePath(exe, installedExe) {
		if err := copyFile(exe, installedExe); err != nil {
			result.Errors = append(result.Errors, "install broker: "+err.Error())
		} else {
			result.InstalledBroker = installedExe
		}
	} else {
		result.InstalledBroker = exe
	}

	root, err := findExtensionRoot()
	if err != nil {
		result.Errors = append(result.Errors, err.Error())
	} else {
		patched, patchErr := patchExtension(root)
		result.Extension = patched
		if patchErr != nil {
			result.Errors = append(result.Errors, "patch extension: "+patchErr.Error())
		}
	}

	if runtime.GOOS == "windows" {
		if err := ensureNode(); err != nil {
			result.Warnings = append(result.Warnings, err.Error())
		}
		if err := registerScheduledTask(installedExe); err != nil {
			result.Warnings = append(result.Warnings, "scheduled task: "+err.Error())
		}
		result.Warnings = append(result.Warnings, installPersistenceFallback(installedExe)...)
	}
	if err := startBrokerDetached(installedExe); err != nil {
		result.Errors = append(result.Errors, "start broker: "+err.Error())
	} else {
		result.BrokerStarted = true
	}

	health := waitBrokerHealth(12 * time.Second)
	result.BrokerHealth = health
	if root != "" {
		result.ReloadAttempted = true
		reload, reloadErr := reloadAndVerifyExtension(extensionID, version)
		result.ReloadEvidence = reload
		result.ReloadVerified = reloadErr == nil
		if reloadErr != nil {
			result.Warnings = append(result.Warnings, "extension reload: "+reloadErr.Error())
			if os.Getenv("MF_RESTART_BROWSER_IF_NEEDED") == "1" {
				cfg, cfgErr := ensureControlConfig()
				if cfgErr == nil && cfg.Browser.RestartBrowser {
					restartEvidence := restartChrome(cfg.Browser)
					if m, ok := result.ReloadEvidence.(map[string]any); ok {
						m["browserRestart"] = restartEvidence
					} else {
						result.ReloadEvidence = map[string]any{"firstAttempt": reload, "browserRestart": restartEvidence}
					}
					if ok, _ := restartEvidence["ok"].(bool); ok {
						time.Sleep(3 * time.Second)
						retry, retryErr := reloadAndVerifyExtension(extensionID, version)
						if m, ok := result.ReloadEvidence.(map[string]any); ok {
							m["postRestartVerification"] = retry
						}
						result.ReloadVerified = retryErr == nil
						if retryErr != nil {
							result.Warnings = append(result.Warnings, "post-restart extension verification: "+retryErr.Error())
						}
					}
				} else if cfgErr != nil {
					result.Warnings = append(result.Warnings, "browser restart config: "+cfgErr.Error())
				}
			}
		}
	}

	if len(result.Errors) == 0 {
		if result.ReloadVerified {
			result.Status = "IMPLEMENTED_RELOADED_VERIFIED"
		} else {
			result.Status = "IMPLEMENTED_RELOAD_DEFERRED"
		}
	}
	result.CompletedAt = time.Now().UTC().Format(time.RFC3339Nano)
	paths := canonicalResultPaths()
	result.ResultPaths = paths
	for _, path := range paths {
		if err := writeJSONAtomic(path, result); err != nil {
			fmt.Fprintln(os.Stderr, "result write failed:", path, err)
		}
	}
	printJSON(result)
	if len(result.Errors) > 0 {
		return errors.New(strings.Join(result.Errors, "; "))
	}
	return nil
}

func doctor() error {
	root, rootErr := findExtensionRoot()
	health := fetchJSON(cdpBase+"/json/version", 2*time.Second)
	broker := fetchJSON(fmt.Sprintf("http://127.0.0.1:%d/health", brokerPort), 3*time.Second)
	out := map[string]any{
		"schema":         "minefield.selfrepair.doctor/1",
		"version":        version,
		"extensionRoot":  root,
		"extensionError": errString(rootErr),
		"cdp":            health,
		"broker":         broker,
		"node":           findNode(),
		"npx":            findNpx(),
	}
	printJSON(out)
	return nil
}

func findExtensionRoot() (string, error) {
	var candidates []string
	if env := os.Getenv("MF_EXTENSION_ROOT"); env != "" {
		candidates = append(candidates, env)
	}
	local := localAppData()
	user := userProfile()
	candidates = append(candidates,
		filepath.Join(local, "DTZipReturn", "extension"),
		filepath.Join(local, "DoubleTab", "MinefieldControl", "extension"),
		filepath.Join(user, "AppData", "Local", "DTZipReturn", "extension"),
		`E:\Transductive_DoubleTab_Codex_MVP\extension`,
		`E:\Transductive_DoubleTab_Codex_MVP\artifacts\extension`,
		filepath.Join(user, "Downloads", "Minefield Control", "extension"),
	)
	seen := map[string]bool{}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		clean := filepath.Clean(candidate)
		if seen[strings.ToLower(clean)] {
			continue
		}
		seen[strings.ToLower(clean)] = true
		if validExtensionRoot(clean) {
			return clean, nil
		}
	}
	// Bounded search under known roots. Avoid walking the entire drive.
	for _, base := range []string{filepath.Join(local, "DTZipReturn"), filepath.Join(local, "DoubleTab"), filepath.Join(user, "Downloads")} {
		if base == "" {
			continue
		}
		found := ""
		_ = filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				rel, _ := filepath.Rel(base, path)
				if strings.Count(rel, string(os.PathSeparator)) > 5 {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.EqualFold(d.Name(), "manifest.json") && validExtensionRoot(filepath.Dir(path)) {
				found = filepath.Dir(path)
				return errors.New("found")
			}
			return nil
		})
		if found != "" {
			return found, nil
		}
	}
	return "", errors.New("active Minefield Control extension root not found")
}

func validExtensionRoot(root string) bool {
	data, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		return false
	}
	var raw map[string]any
	if json.Unmarshal(data, &raw) != nil {
		return false
	}
	name := strings.ToLower(stringAny(raw["name"]))
	if strings.Contains(name, "minefield") {
		return true
	}
	for _, file := range []string{"background.js", "watchdog.js"} {
		b, _ := os.ReadFile(filepath.Join(root, file))
		if bytes.Contains(b, []byte("mfRunWatchdogInvestigator")) || bytes.Contains(b, []byte("MINEFIELD")) {
			return true
		}
	}
	return false
}

func patchExtension(root string) (*PatchResult, error) {
	manifestPath := filepath.Join(root, "manifest.json")
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, err
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return nil, err
	}
	result := &PatchResult{Root: root, ManifestPath: manifestPath, OriginalVersion: manifest.Version, PatchedVersion: version, WrittenFiles: map[string]string{}}
	if bytes.Contains(manifestBytes, []byte(version)) {
		if _, err := os.Stat(filepath.Join(root, "mf_contract_core_v200.js")); err == nil {
			if _, err := os.Stat(filepath.Join(root, "mf_self_repair_background_v200.js")); err == nil {
				result.AlreadyPatched = true
			}
		}
	}
	backupDir := filepath.Join(localAppData(), "DoubleTab", "MinefieldControl", "backups")
	_ = os.MkdirAll(backupDir, 0o755)
	backup := filepath.Join(backupDir, fmt.Sprintf("Minefield-Control-pre-v%s-%s.zip", version, time.Now().UTC().Format("20060102T150405Z")))
	if err := zipDir(root, backup); err != nil {
		return result, fmt.Errorf("backup: %w", err)
	}
	result.BackupPath = backup

	bgFile, bgType, err := resolveBackground(manifest)
	if err != nil {
		return result, err
	}
	result.BackgroundFile = bgFile
	bgPath := filepath.Join(root, filepath.FromSlash(bgFile))
	bg, err := os.ReadFile(bgPath)
	if err != nil {
		return result, err
	}
	if !bytes.Contains(bg, []byte(patchMarker)) {
		var suffix string
		if bgType == "module" {
			suffix = fmt.Sprintf("\n/* %s */\nimport './mf_contract_core_v200.js';\nimport './mf_self_repair_background_v200.js';\n", patchMarker)
		} else {
			suffix = fmt.Sprintf("\n/* %s */\ntry { importScripts(chrome.runtime.getURL('mf_contract_core_v200.js'), chrome.runtime.getURL('mf_self_repair_background_v200.js')); } catch (error) { console.error('MF_ARTIFACT_MESH_IMPORT_FAILED', error); }\n", patchMarker)
		}
		if err := writeAtomic(bgPath, append(bg, []byte(suffix)...)); err != nil {
			return result, err
		}
	}

	guarded := map[string]bool{}
	for bundleIndex, cs := range manifest.ContentScripts {
		for fileIndex, jsFile := range cs.JS {
			if jsFile == "mf_contract_core_v200.js" || jsFile == "mf_self_repair_content_v200.js" || guarded[jsFile] {
				continue
			}
			path := filepath.Join(root, filepath.FromSlash(jsFile))
			data, err := os.ReadFile(path)
			if err != nil {
				result.Warnings = append(result.Warnings, "content file missing: "+jsFile)
				continue
			}
			if bytes.Contains(data, []byte(legacyGuardMark)) {
				guarded[jsFile] = true
				result.GuardedContentFiles = append(result.GuardedContentFiles, jsFile)
				continue
			}
			prefix := legacyGuard(bundleIndex, fileIndex == 0)
			if err := writeAtomic(path, append([]byte(prefix), data...)); err != nil {
				return result, err
			}
			guarded[jsFile] = true
			result.GuardedContentFiles = append(result.GuardedContentFiles, jsFile)
		}
	}

	if err := writeAtomic(filepath.Join(root, "mf_contract_core_v200.js"), contractCoreJS); err != nil {
		return result, err
	}
	if err := writeAtomic(filepath.Join(root, "mf_self_repair_background_v200.js"), renderBackgroundWithToken(backgroundJS)); err != nil {
		return result, err
	}
	if err := writeAtomic(filepath.Join(root, "mf_self_repair_content_v200.js"), contentJS); err != nil {
		return result, err
	}

	patchedManifest, err := patchManifestPreservingUnknown(manifestBytes)
	if err != nil {
		return result, err
	}
	if err := writeAtomic(manifestPath, patchedManifest); err != nil {
		return result, err
	}

	meta := map[string]any{
		"schema":              "minefield.selfrepair.patch-manifest/1",
		"version":             version,
		"extensionId":         extensionID,
		"patchedAt":           time.Now().UTC().Format(time.RFC3339Nano),
		"backup":              backup,
		"background":          bgFile,
		"guardedContentFiles": result.GuardedContentFiles,
		"mcp":                 map[string]any{"package": mcpPackage, "browserUrl": cdpBase, "broker": fmt.Sprintf("http://127.0.0.1:%d", brokerPort)},
	}
	metaPath := filepath.Join(root, "mf_self_repair_manifest_v200.json")
	if err := writeJSONAtomic(metaPath, meta); err != nil {
		return result, err
	}
	for _, file := range []string{bgPath, manifestPath, filepath.Join(root, "mf_contract_core_v200.js"), filepath.Join(root, "mf_self_repair_background_v200.js"), filepath.Join(root, "mf_self_repair_content_v200.js"), metaPath} {
		if hash, err := fileSHA256(file); err == nil {
			result.WrittenFiles[file] = hash
		}
	}
	return result, nil
}

func resolveBackground(m Manifest) (file, typ string, err error) {
	if m.Background == nil {
		return "", "", errors.New("manifest has no background service worker")
	}
	if sw, ok := m.Background["service_worker"].(string); ok && sw != "" {
		t, _ := m.Background["type"].(string)
		return sw, t, nil
	}
	if scripts, ok := m.Background["scripts"].([]any); ok && len(scripts) > 0 {
		if s, ok := scripts[len(scripts)-1].(string); ok {
			return s, "classic", nil
		}
	}
	return "", "", errors.New("unable to resolve background entry")
}

func legacyGuard(bundle int, first bool) string {
	key := fmt.Sprintf("doubletab.minefield.legacy-content.v200.bundle.%d", bundle)
	if first {
		return fmt.Sprintf(`/* %s */
;(() => {
  const __mfKey = Symbol.for(%q);
  const __mfGeneration = String(Math.trunc(performance.timeOrigin || Date.now())) + ':' + location.href;
  const __mfExisting = globalThis[__mfKey];
  if (__mfExisting && __mfExisting.generation === __mfGeneration) {
    globalThis.__mfLegacyDuplicateV200_%d = true;
    try { chrome.runtime.sendMessage({kind:'mf.selfrepair.duplicate-legacy-load', generation:__mfGeneration, bundle:%d, at:Date.now()}).catch(()=>{}); } catch (_) {}
    throw new Error('MF_DUPLICATE_CONTENT_ACTOR_SUPPRESSED');
  }
  globalThis.__mfLegacyDuplicateV200_%d = false;
  globalThis[__mfKey] = {generation:__mfGeneration, loadedAt:Date.now()};
})();
`, legacyGuardMark, key, bundle, bundle, bundle)
	}
	return fmt.Sprintf(`/* %s */
if (globalThis.__mfLegacyDuplicateV200_%d === true) { throw new Error('MF_DUPLICATE_CONTENT_BUNDLE_SUPPRESSED'); }
`, legacyGuardMark, bundle)
}

func patchManifestPreservingUnknown(original []byte) ([]byte, error) {
	var doc map[string]any
	if err := json.Unmarshal(original, &doc); err != nil {
		return nil, err
	}
	doc["version"] = version
	if value, ok := doc["version_name"].(string); ok && value != "" && !strings.Contains(value, "SelfRepair") {
		doc["version_name"] = strings.TrimSpace(value + " + SelfRepair")
	}
	permissions := anyStringSlice(doc["permissions"])
	doc["permissions"] = addUnique(permissions, "storage", "tabs", "scripting", "alarms", "debugger", "downloads", "webNavigation", "management")
	hostPermissions := anyStringSlice(doc["host_permissions"])
	doc["host_permissions"] = addUnique(hostPermissions, "http://127.0.0.1/*", "http://localhost/*")

	contentScripts, _ := doc["content_scripts"].([]any)
	hasSelfRepair := false
	for _, raw := range contentScripts {
		entry, _ := raw.(map[string]any)
		for _, file := range anyStringSlice(entry["js"]) {
			if file == "mf_self_repair_content_v200.js" {
				hasSelfRepair = true
			}
		}
	}
	if !hasSelfRepair {
		contentScripts = append(contentScripts, map[string]any{
			"matches":    []string{"https://chatgpt.com/*", "https://chat.openai.com/*"},
			"js":         []string{"mf_contract_core_v200.js", "mf_self_repair_content_v200.js"},
			"run_at":     "document_start",
			"all_frames": false,
			"world":      "ISOLATED",
		})
	}
	doc["content_scripts"] = contentScripts
	return json.MarshalIndent(doc, "", "  ")
}

func anyStringSlice(value any) []string {
	items, _ := value.([]any)
	out := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			out = append(out, text)
		}
	}
	if direct, ok := value.([]string); ok {
		return direct
	}
	return out
}

func hasSelfRepairContent(scripts []ContentScript) bool {
	for _, cs := range scripts {
		for _, js := range cs.JS {
			if js == "mf_self_repair_content_v200.js" {
				return true
			}
		}
	}
	return false
}

func addUnique(values []string, extra ...string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values)+len(extra))
	for _, v := range append(values, extra...) {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

func runBroker() error {
	root := filepath.Join(localAppData(), "DoubleTab", "MinefieldArtifactMesh")
	if err := os.MkdirAll(filepath.Join(root, "diagnostics"), 0o755); err != nil {
		return err
	}
	logFile, _ := os.OpenFile(filepath.Join(root, "broker.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if logFile != nil {
		defer logFile.Close()
		log.SetOutput(io.MultiWriter(os.Stderr, logFile))
	}
	b := newBroker(root)
	control, controlErr := newControlPlane(root)
	if controlErr != nil {
		log.Println("total-control initialization degraded:", controlErr)
	} else {
		b.control = control
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", b.handleHealth)
	mux.HandleFunc("/mcp/start", b.handleMCPStart)
	mux.HandleFunc("/mcp/tools", b.handleMCPTools)
	mux.HandleFunc("/mcp/call", b.handleMCPCall)
	mux.HandleFunc("/incident", b.handleIncident)
	mux.HandleFunc("/extension/reload", b.handleReload)
	if control != nil {
		mux.HandleFunc("/control/status", control.handleStatus)
		mux.HandleFunc("/control/config", control.handleConfig)
		mux.HandleFunc("/control/execute", control.handleExecute)
		mux.HandleFunc("/control/events", control.handleEvents)
		mux.HandleFunc("/artifact/register", control.handleArtifactRegister)
		mux.HandleFunc("/return/pending", control.handleReturnPending)
		mux.HandleFunc("/return/ack", control.handleReturnAck)
		mux.HandleFunc("/locator/register", control.handleLocatorRegister)
		mux.HandleFunc("/locator/resolve", control.handleLocatorResolve)
	}
	server := &http.Server{Addr: fmt.Sprintf("127.0.0.1:%d", brokerPort), Handler: corsLocal(mux), ReadHeaderTimeout: 5 * time.Second}
	log.Printf("Minefield DevTools broker v%s listening on %s", version, server.Addr)
	// DevTools MCP starts on demand. Keeping broker startup independent prevents
	// an unavailable npm registry or CDP endpoint from blocking /health and all
	// local-control capabilities.
	return server.ListenAndServe()
}

type Broker struct {
	root          string
	started       time.Time
	mcpMu         sync.Mutex
	mcp           *MCPClient
	lastMCPError  string
	incidentCount atomic.Int64
	control       *ControlPlane
}

func newBroker(root string) *Broker { return &Broker{root: root, started: time.Now()} }

func (b *Broker) startMCP() error {
	b.mcpMu.Lock()
	defer b.mcpMu.Unlock()
	if b.mcp != nil && b.mcp.Alive() {
		return nil
	}
	npx := findNpx()
	if npx == "" {
		b.lastMCPError = "npx not found"
		return errors.New(b.lastMCPError)
	}
	args := []string{"-y", mcpPackage, "--browser-url=" + cdpBase, "--no-usage-statistics", "--no-performance-crux"}
	client, err := StartMCP(npx, args, filepath.Join(b.root, "chrome-devtools-mcp.log"))
	if err != nil {
		b.lastMCPError = err.Error()
		return err
	}
	b.mcp = client
	b.lastMCPError = ""
	return nil
}

func (b *Broker) health() map[string]any {
	b.mcpMu.Lock()
	mcp := b.mcp
	mcpErr := b.lastMCPError
	b.mcpMu.Unlock()
	cdp := fetchJSON(cdpBase+"/json/version", 1500*time.Millisecond)
	alive := mcp != nil && mcp.Alive()
	tools := 0
	if alive {
		tools = len(mcp.ToolNames())
	}
	out := map[string]any{
		"ok":            true,
		"schema":        "minefield.devtools-broker.health/1",
		"version":       version,
		"uptimeSeconds": int(time.Since(b.started).Seconds()),
		"cdp":           cdp,
		"mcp":           map[string]any{"alive": alive, "package": mcpPackage, "tools": tools, "lastError": mcpErr},
		"incidents":     b.incidentCount.Load(),
	}
	if b.control != nil {
		out["totalControl"] = b.control.status()
	}
	return out
}

func (b *Broker) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeHTTPJSON(w, 200, b.health())
}
func (b *Broker) handleMCPStart(w http.ResponseWriter, r *http.Request) {
	err := b.startMCP()
	if err != nil {
		writeHTTPJSON(w, 503, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeHTTPJSON(w, 200, b.health())
}
func (b *Broker) handleMCPTools(w http.ResponseWriter, r *http.Request) {
	if err := b.startMCP(); err != nil {
		writeHTTPJSON(w, 503, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	b.mcpMu.Lock()
	mcp := b.mcp
	b.mcpMu.Unlock()
	writeHTTPJSON(w, 200, map[string]any{"ok": true, "tools": mcp.Tools()})
}
func (b *Broker) handleMCPCall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeHTTPJSON(w, 405, map[string]any{"ok": false, "error": "POST required"})
		return
	}
	var req struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&req); err != nil {
		writeHTTPJSON(w, 400, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if err := b.startMCP(); err != nil {
		writeHTTPJSON(w, 503, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	b.mcpMu.Lock()
	mcp := b.mcp
	b.mcpMu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	res, err := mcp.CallTool(ctx, req.Name, req.Arguments)
	if err != nil {
		writeHTTPJSON(w, 500, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeHTTPJSON(w, 200, map[string]any{"ok": true, "result": res})
}
func (b *Broker) handleReload(w http.ResponseWriter, r *http.Request) {
	evidence, err := reloadAndVerifyExtension(extensionID, version)
	if err != nil {
		writeHTTPJSON(w, 500, map[string]any{"ok": false, "error": err.Error(), "evidence": evidence})
		return
	}
	writeHTTPJSON(w, 200, map[string]any{"ok": true, "evidence": evidence})
}
func (b *Broker) handleIncident(w http.ResponseWriter, r *http.Request) {
	if b.control != nil {
		if err := b.control.authorize(r); err != nil {
			writeHTTPJSON(w, 401, map[string]any{"ok": false, "error": err.Error()})
			return
		}
	}
	if r.Method != http.MethodPost {
		writeHTTPJSON(w, 405, map[string]any{"ok": false, "error": "POST required"})
		return
	}
	var incident map[string]any
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<20)).Decode(&incident); err != nil {
		writeHTTPJSON(w, 400, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	id := fmt.Sprintf("incident-%s-%06d", time.Now().UTC().Format("20060102T150405Z"), b.incidentCount.Add(1))
	path := filepath.Join(b.root, "diagnostics", id+".json")
	record := map[string]any{"id": id, "receivedAt": time.Now().UTC().Format(time.RFC3339Nano), "incident": incident, "brokerHealth": b.health(), "cdpTargets": fetchJSON(cdpBase+"/json/list", 3*time.Second)}
	_ = writeJSONAtomic(path, record)
	go b.processIncident(id, incident, path)
	writeHTTPJSON(w, 202, map[string]any{"ok": true, "accepted": true, "id": id, "path": path})
}

func (b *Broker) processIncident(id string, incident map[string]any, path string) {
	evidence := map[string]any{}
	if err := b.startMCP(); err == nil {
		b.mcpMu.Lock()
		mcp := b.mcp
		b.mcpMu.Unlock()
		for _, tool := range []string{"list_pages", "list_console_messages", "list_network_requests", "take_snapshot"} {
			if !mcp.HasTool(tool) {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			res, err := mcp.CallTool(ctx, tool, map[string]any{})
			cancel()
			if err != nil {
				evidence[tool] = map[string]any{"error": err.Error()}
			} else {
				evidence[tool] = res
			}
		}
	} else {
		evidence["mcp"] = map[string]any{"error": err.Error()}
	}
	prompt := buildMaintenancePrompt(id, incident, evidence)
	diagnosis, diagErr := runMaintenanceChat(prompt, 4*time.Minute)
	update := map[string]any{"processedAt": time.Now().UTC().Format(time.RFC3339Nano), "devtoolsEvidence": evidence, "maintenance": diagnosis}
	if diagErr != nil {
		update["maintenanceError"] = diagErr.Error()
	}
	var record map[string]any
	data, _ := os.ReadFile(path)
	_ = json.Unmarshal(data, &record)
	for k, v := range update {
		record[k] = v
	}
	_ = writeJSONAtomic(path, record)
	_ = writeJSONAtomic(filepath.Join(b.root, "diagnostics", "MINEFIELD__DIAGNOSTIC__LATEST.json"), record)
	if envelope := extractRepairEnvelope(fmt.Sprint(diagnosis)); envelope != nil {
		record["boundedRepairExecution"] = executeBoundedRepair(envelope)
		_ = writeJSONAtomic(path, record)
		_ = writeJSONAtomic(filepath.Join(b.root, "diagnostics", "MINEFIELD__DIAGNOSTIC__LATEST.json"), record)
	}
	b.emitIncidentResult(id, incident, path, record)
}

func (b *Broker) emitIncidentResult(id string, incident map[string]any, diagnosticPath string, record map[string]any) {
	if b.control == nil {
		return
	}
	originURL := strings.TrimSpace(stringAny(incident["originUrl"]))
	if originURL == "" || !b.control.authorizedOrigin(originURL) {
		return
	}
	payload := map[string]any{
		"schema": "minefield.runtime-feedback/1", "incidentId": id, "status": "PROCESSED",
		"diagnosticPath": diagnosticPath, "processedAt": record["processedAt"],
		"maintenance": record["maintenance"], "maintenanceError": record["maintenanceError"],
		"boundedRepairExecution": record["boundedRepairExecution"],
	}
	env := ReturnEnvelope{
		State: "READY", Schema: "minefield.return/1", ID: "return-" + id, Kind: "runtime.incident.result",
		OriginURL: originURL, ConversationKey: strings.TrimSpace(stringAny(incident["conversationKey"])),
		TabActorID: strings.TrimSpace(stringAny(incident["tabActorId"])), TabID: intAny(incident["tabId"]),
		Payload: payload, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := b.control.emitReturn(env); err != nil {
		b.control.recordEvent("runtime.feedback.return-failed", map[string]any{"incidentId": id, "error": err.Error()})
	} else {
		b.control.recordEvent("runtime.feedback.return-ready", map[string]any{"incidentId": id, "originUrl": originURL})
	}
}

func buildMaintenancePrompt(id string, incident map[string]any, evidence map[string]any) string {
	payload, _ := json.Marshal(map[string]any{"incidentId": id, "incident": incident, "devtoolsEvidence": evidence})
	if len(payload) > 50000 {
		payload = payload[:50000]
	}
	return `You are the dedicated Minefield maintenance actor. Diagnose the attached typed incident using only the evidence supplied. Preserve human drafts, unrelated tabs, credentials and durable identities. Distinguish WAITING_FOR_YOU, PAUSED, NEEDS_REPAIR and FAILED. Return exactly one bounded contract and no prose outside it:
[[MINEFIELD_REPAIR/1]]
{"diagnosis":"...","state":"NEEDS_REPAIR","actions":[{"action":"rerun-watchdog"}],"evidence":"..."}
[[/MINEFIELD_REPAIR]]
Allowed actions only: rerun-watchdog, reload-extension, restart-mcp, no-op. Never return arbitrary code or request destructive browser/profile changes.

[[MINEFIELD_DIAGNOSTIC/1]]
` + string(payload) + `
[[/MINEFIELD_DIAGNOSTIC]]`
}

func extractRepairEnvelope(text string) map[string]any {
	start := strings.Index(text, "[[MINEFIELD_REPAIR/1]]")
	end := strings.Index(text, "[[/MINEFIELD_REPAIR]]")
	if start < 0 || end <= start {
		return nil
	}
	body := strings.TrimSpace(text[start+len("[[MINEFIELD_REPAIR/1]]") : end])
	var out map[string]any
	if json.Unmarshal([]byte(body), &out) != nil {
		return nil
	}
	return out
}

func executeBoundedRepair(envelope map[string]any) []map[string]any {
	var results []map[string]any
	actions, _ := envelope["actions"].([]any)
	for _, raw := range actions {
		item, _ := raw.(map[string]any)
		action := stringAny(item["action"])
		entry := map[string]any{"action": action, "at": time.Now().UTC().Format(time.RFC3339Nano)}
		switch action {
		case "reload-extension":
			evidence, err := reloadAndVerifyExtension(extensionID, version)
			entry["evidence"] = evidence
			entry["ok"] = err == nil
			if err != nil {
				entry["error"] = err.Error()
			}
		case "rerun-watchdog":
			target, err := findExtensionTarget(extensionID)
			if err == nil {
				res, callErr := cdpEvaluate(target.WebSocketDebuggerURL, `globalThis.mfSelfRepairV200?.investigate('maintenance-contract')`, true, 30*time.Second)
				entry["result"] = res
				entry["ok"] = callErr == nil
				if callErr != nil {
					entry["error"] = callErr.Error()
				}
			} else {
				entry["ok"] = false
				entry["error"] = err.Error()
			}
		case "restart-mcp":
			entry["ok"] = true
			entry["note"] = "broker will restart MCP on next call"
		case "no-op":
			entry["ok"] = true
		default:
			entry["ok"] = false
			entry["error"] = "ACTION_NOT_ALLOWED"
		}
		results = append(results, entry)
	}
	return results
}

func runMaintenanceChat(prompt string, timeout time.Duration) (map[string]any, error) {
	browserWS, err := browserWebSocketURL()
	if err != nil {
		return nil, err
	}
	created, err := cdpCall(browserWS, "Target.createTarget", map[string]any{"url": "https://chatgpt.com/"}, 15*time.Second)
	if err != nil {
		return nil, err
	}
	targetID := nestedString(created, "result", "targetId")
	if targetID == "" {
		return nil, errors.New("Target.createTarget returned no targetId")
	}
	var target CDPTarget
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		targets, _ := listTargets()
		for _, t := range targets {
			if t.ID == targetID {
				target = t
				break
			}
		}
		if target.WebSocketDebuggerURL != "" {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if target.WebSocketDebuggerURL == "" {
		return nil, errors.New("maintenance page target unavailable")
	}
	encoded := strconv.Quote(prompt)
	expression := `(async()=>{const sleep=m=>new Promise(r=>setTimeout(r,m));let c=null;for(let i=0;i<120;i++){c=document.querySelector('#prompt-textarea,[data-testid="composer-text-input"],[contenteditable="true"][role="textbox"],textarea[placeholder]');if(c)break;await sleep(250);}if(!c)return {ok:false,error:'COMPOSER_NOT_FOUND',url:location.href};const draft=('value'in c?c.value:(c.innerText||c.textContent||''));if(String(draft).trim())return {ok:false,error:'HUMAN_DRAFT_PRESENT'};const text=` + encoded + `;c.focus();if('value'in c){const s=Object.getOwnPropertyDescriptor(Object.getPrototypeOf(c),'value')?.set;s?s.call(c,text):c.value=text;c.dispatchEvent(new Event('input',{bubbles:true,composed:true}));}else{c.textContent=text;c.dispatchEvent(new InputEvent('input',{bubbles:true,composed:true,inputType:'insertText',data:text}));}await sleep(250);const b=document.querySelector('[data-testid="send-button"],button[aria-label*="Send" i],form button[type="submit"]');if(b&&!b.disabled){b.click();return {ok:true,method:'button'};}c.dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',code:'Enter',bubbles:true}));return {ok:true,method:'enter'};})()`
	submit, err := cdpEvaluate(target.WebSocketDebuggerURL, expression, true, 45*time.Second)
	if err != nil {
		return map[string]any{"targetId": targetID, "submit": submit}, err
	}
	pollDeadline := time.Now().Add(timeout)
	for time.Now().Before(pollDeadline) {
		time.Sleep(4 * time.Second)
		readExpr := `(()=>{const n=[...document.querySelectorAll('[data-message-author-role="assistant"],article')];const x=n[n.length-1];const t=String(x?.innerText||x?.textContent||'');return {count:n.length,text:t.slice(-24000),done:t.includes('[[/MINEFIELD_REPAIR]]')};})()`
		res, readErr := cdpEvaluate(target.WebSocketDebuggerURL, readExpr, true, 20*time.Second)
		if readErr != nil {
			continue
		}
		text := extractRuntimeValueString(res, "text")
		if strings.Contains(text, "[[/MINEFIELD_REPAIR]]") {
			return map[string]any{"targetId": targetID, "submit": submit, "response": text}, nil
		}
	}
	return map[string]any{"targetId": targetID, "submit": submit}, errors.New("maintenance chat timed out")
}

// MCP stdio client.
type MCPClient struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	mu      sync.Mutex
	pending map[int64]chan json.RawMessage
	seq     atomic.Int64
	alive   atomic.Bool
	toolsMu sync.RWMutex
	tools   []map[string]any
}

func StartMCP(command string, args []string, logPath string) (*MCPClient, error) {
	cmd := exec.Command(command, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	logFile, _ := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if logFile != nil {
		cmd.Stderr = logFile
	} else {
		cmd.Stderr = os.Stderr
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	c := &MCPClient{cmd: cmd, stdin: stdin, pending: map[int64]chan json.RawMessage{}}
	c.alive.Store(true)
	go c.readLoop(stdout)
	go func() {
		_ = cmd.Wait()
		c.alive.Store(false)
		if logFile != nil {
			_ = logFile.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	initRes, err := c.request(ctx, "initialize", map[string]any{
		"protocolVersion": "2025-03-26",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "MinefieldDevToolsBroker", "version": version},
	})
	if err != nil {
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("MCP initialize: %w", err)
	}
	_ = initRes
	_ = c.notify("notifications/initialized", map[string]any{})
	toolsRes, err := c.request(ctx, "tools/list", map[string]any{})
	if err != nil {
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("MCP tools/list: %w", err)
	}
	var decoded map[string]any
	_ = json.Unmarshal(toolsRes, &decoded)
	if list, ok := decoded["tools"].([]any); ok {
		var tools []map[string]any
		for _, item := range list {
			if m, ok := item.(map[string]any); ok {
				tools = append(tools, m)
			}
		}
		c.toolsMu.Lock()
		c.tools = tools
		c.toolsMu.Unlock()
	}
	return c, nil
}

func (c *MCPClient) readLoop(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 32<<20)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  any             `json:"error"`
		}
		if json.Unmarshal(line, &msg) != nil || len(msg.ID) == 0 {
			continue
		}
		var id int64
		if json.Unmarshal(msg.ID, &id) != nil {
			continue
		}
		payload := msg.Result
		if msg.Error != nil {
			payload, _ = json.Marshal(map[string]any{"__error": msg.Error})
		}
		c.mu.Lock()
		ch := c.pending[id]
		delete(c.pending, id)
		c.mu.Unlock()
		if ch != nil {
			ch <- payload
			close(ch)
		}
	}
	c.alive.Store(false)
}

func (c *MCPClient) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if !c.Alive() {
		return nil, errors.New("MCP process not alive")
	}
	id := c.seq.Add(1)
	ch := make(chan json.RawMessage, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	msg := map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
	data, _ := json.Marshal(msg)
	c.mu.Lock()
	_, err := c.stdin.Write(append(data, '\n'))
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	case raw := <-ch:
		var probe map[string]any
		if json.Unmarshal(raw, &probe) == nil {
			if e, ok := probe["__error"]; ok {
				return nil, fmt.Errorf("MCP error: %v", e)
			}
		}
		return raw, nil
	}
}
func (c *MCPClient) notify(method string, params any) error {
	data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err := c.stdin.Write(append(data, '\n'))
	return err
}
func (c *MCPClient) CallTool(ctx context.Context, name string, args map[string]any) (any, error) {
	raw, err := c.request(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return nil, err
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return string(raw), nil
	}
	return out, nil
}
func (c *MCPClient) Alive() bool { return c != nil && c.alive.Load() }
func (c *MCPClient) Tools() []map[string]any {
	c.toolsMu.RLock()
	defer c.toolsMu.RUnlock()
	return append([]map[string]any(nil), c.tools...)
}
func (c *MCPClient) ToolNames() []string {
	var out []string
	for _, t := range c.Tools() {
		out = append(out, stringAny(t["name"]))
	}
	return out
}
func (c *MCPClient) HasTool(name string) bool {
	for _, n := range c.ToolNames() {
		if n == name {
			return true
		}
	}
	return false
}

// Minimal CDP/WebSocket client.
type CDPTarget struct {
	ID                   string `json:"id"`
	Type                 string `json:"type"`
	Title                string `json:"title"`
	URL                  string `json:"url"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

func listTargets() ([]CDPTarget, error) {
	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Get(cdpBase + "/json/list")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var targets []CDPTarget
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&targets); err != nil {
		return nil, err
	}
	return targets, nil
}
func findExtensionTarget(id string) (CDPTarget, error) {
	targets, err := listTargets()
	if err != nil {
		return CDPTarget{}, err
	}
	needle := "chrome-extension://" + id + "/"
	for _, t := range targets {
		if strings.HasPrefix(t.URL, needle) && (t.Type == "service_worker" || t.Type == "background_page" || t.Type == "page") {
			return t, nil
		}
	}
	return CDPTarget{}, errors.New("extension target not found through CDP")
}
func browserWebSocketURL() (string, error) {
	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Get(cdpBase + "/json/version")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var v map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return "", err
	}
	ws := stringAny(v["webSocketDebuggerUrl"])
	if ws == "" {
		return "", errors.New("browser websocket URL missing")
	}
	return ws, nil
}
func reloadAndVerifyExtension(id, expectedVersion string) (map[string]any, error) {
	evidence := map[string]any{"extensionId": id, "expectedVersion": expectedVersion, "startedAt": time.Now().UTC().Format(time.RFC3339Nano)}
	target, err := findExtensionTarget(id)
	if err != nil {
		return evidence, err
	}
	evidence["preTarget"] = target
	_, err = cdpEvaluate(target.WebSocketDebuggerURL, `chrome.runtime.reload(); 'reload-requested'`, true, 10*time.Second)
	if err != nil {
		return evidence, err
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(700 * time.Millisecond)
		newTarget, findErr := findExtensionTarget(id)
		if findErr != nil {
			continue
		}
		res, evalErr := cdpEvaluate(newTarget.WebSocketDebuggerURL, `({version:chrome.runtime.getManifest().version,selfRepair:Boolean(globalThis.mfSelfRepairV200),loaded:Boolean(globalThis.__mfSelfRepairBackgroundV200)})`, true, 8*time.Second)
		if evalErr != nil {
			continue
		}
		evidence["postTarget"] = newTarget
		evidence["runtime"] = res
		encoded, _ := json.Marshal(res)
		if bytes.Contains(encoded, []byte(expectedVersion)) && (bytes.Contains(encoded, []byte("selfRepair")) || bytes.Contains(encoded, []byte("loaded"))) {
			evidence["verifiedAt"] = time.Now().UTC().Format(time.RFC3339Nano)
			return evidence, nil
		}
	}
	return evidence, errors.New("extension reload was requested but v2.0.1 runtime verification timed out")
}
func cdpEvaluate(wsURL, expression string, await bool, timeout time.Duration) (map[string]any, error) {
	return cdpCall(wsURL, "Runtime.evaluate", map[string]any{"expression": expression, "returnByValue": true, "awaitPromise": await, "userGesture": true}, timeout)
}
func cdpCall(wsURL, method string, params map[string]any, timeout time.Duration) (map[string]any, error) {
	ws, err := dialWebSocket(wsURL, timeout)
	if err != nil {
		return nil, err
	}
	defer ws.Close()
	id := int64(1)
	request, _ := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if err := ws.WriteText(request); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		_ = ws.conn.SetReadDeadline(deadline)
		data, err := ws.ReadText()
		if err != nil {
			return nil, err
		}
		var msg map[string]any
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		if int64FromAny(msg["id"]) == id {
			if e, ok := msg["error"]; ok {
				return msg, fmt.Errorf("CDP error: %v", e)
			}
			return msg, nil
		}
	}
	return nil, errors.New("CDP timeout")
}

type wsClient struct {
	conn   net.Conn
	reader *bufio.Reader
}

func dialWebSocket(raw string, timeout time.Duration) (*wsClient, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	host := u.Host
	if !strings.Contains(host, ":") {
		if u.Scheme == "wss" {
			host += ":443"
		} else {
			host += ":80"
		}
	}
	if u.Scheme == "wss" {
		return nil, errors.New("wss not supported for local CDP")
	}
	conn, err := net.DialTimeout("tcp", host, timeout)
	if err != nil {
		return nil, err
	}
	keyBytes := make([]byte, 16)
	_, _ = rand.Read(keyBytes)
	key := base64.StdEncoding.EncodeToString(keyBytes)
	path := u.RequestURI()
	if path == "" {
		path = "/"
	}
	req := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n", path, u.Host, key)
	if _, err := io.WriteString(conn, req); err != nil {
		conn.Close()
		return nil, err
	}
	reader := bufio.NewReader(conn)
	status, err := reader.ReadString('\n')
	if err != nil {
		conn.Close()
		return nil, err
	}
	if !strings.Contains(status, "101") {
		conn.Close()
		return nil, fmt.Errorf("websocket upgrade failed: %s", strings.TrimSpace(status))
	}
	headers := map[string]string{}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			conn.Close()
			return nil, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 2 {
			headers[strings.ToLower(strings.TrimSpace(parts[0]))] = strings.TrimSpace(parts[1])
		}
	}
	acceptWant := base64.StdEncoding.EncodeToString(sha1sum(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	if !strings.EqualFold(headers["sec-websocket-accept"], acceptWant) {
		conn.Close()
		return nil, errors.New("invalid websocket accept")
	}
	return &wsClient{conn: conn, reader: reader}, nil
}
func sha1sum(s string) []byte    { h := sha1.Sum([]byte(s)); return h[:] }
func (w *wsClient) Close() error { return w.conn.Close() }
func (w *wsClient) WriteText(data []byte) error {
	var header []byte
	n := len(data)
	header = append(header, 0x81)
	mask := make([]byte, 4)
	_, _ = rand.Read(mask)
	if n < 126 {
		header = append(header, byte(n)|0x80)
	} else if n <= math.MaxUint16 {
		header = append(header, 126|0x80, byte(n>>8), byte(n))
	} else {
		header = append(header, 127|0x80)
		b := make([]byte, 8)
		binary.BigEndian.PutUint64(b, uint64(n))
		header = append(header, b...)
	}
	header = append(header, mask...)
	payload := make([]byte, n)
	for i := range data {
		payload[i] = data[i] ^ mask[i%4]
	}
	_, err := w.conn.Write(append(header, payload...))
	return err
}
func (w *wsClient) ReadText() ([]byte, error) {
	for {
		b1, err := w.reader.ReadByte()
		if err != nil {
			return nil, err
		}
		b2, err := w.reader.ReadByte()
		if err != nil {
			return nil, err
		}
		op := b1 & 0x0f
		masked := b2&0x80 != 0
		n := uint64(b2 & 0x7f)
		if n == 126 {
			x := make([]byte, 2)
			if _, err = io.ReadFull(w.reader, x); err != nil {
				return nil, err
			}
			n = uint64(binary.BigEndian.Uint16(x))
		} else if n == 127 {
			x := make([]byte, 8)
			if _, err = io.ReadFull(w.reader, x); err != nil {
				return nil, err
			}
			n = binary.BigEndian.Uint64(x)
		}
		if n > 32<<20 {
			return nil, errors.New("websocket frame too large")
		}
		mask := make([]byte, 4)
		if masked {
			if _, err = io.ReadFull(w.reader, mask); err != nil {
				return nil, err
			}
		}
		payload := make([]byte, int(n))
		if _, err = io.ReadFull(w.reader, payload); err != nil {
			return nil, err
		}
		if masked {
			for i := range payload {
				payload[i] ^= mask[i%4]
			}
		}
		if op == 0x9 {
			_ = w.writeControl(0xA, payload)
			continue
		}
		if op == 0x8 {
			return nil, io.EOF
		}
		if op == 0x1 {
			return payload, nil
		}
	}
}
func (w *wsClient) writeControl(op byte, payload []byte) error {
	frame := []byte{0x80 | op, byte(len(payload))}
	_, err := w.conn.Write(append(frame, payload...))
	return err
}

func nestedString(m map[string]any, keys ...string) string {
	var cur any = m
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = mm[k]
	}
	return stringAny(cur)
}
func extractRuntimeValueString(m map[string]any, field string) string {
	result, _ := m["result"].(map[string]any)
	inner, _ := result["result"].(map[string]any)
	value, _ := inner["value"].(map[string]any)
	return stringAny(value[field])
}
func int64FromAny(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case int64:
		return x
	case json.Number:
		i, _ := x.Int64()
		return i
	}
	return 0
}

func ensureNode() error {
	if findNode() != "" && findNpx() != "" {
		return nil
	}
	winget := findCommand([]string{"winget.exe", "winget"})
	if winget == "" {
		return errors.New("Node.js/npm not found and winget unavailable; direct CDP fallback remains active but Chrome DevTools MCP is degraded")
	}
	cmd := exec.Command(winget, "install", "--id", "OpenJS.NodeJS.LTS", "--exact", "--silent", "--accept-package-agreements", "--accept-source-agreements")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("Node.js LTS install failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
func registerScheduledTask(exe string) error {
	schtasks := findCommand([]string{"schtasks.exe", "schtasks"})
	if schtasks == "" {
		return errors.New("schtasks not found")
	}
	taskCmd := fmt.Sprintf("\"%s\" broker", exe)
	cmd := exec.Command(schtasks, "/Create", "/TN", "DoubleTab Minefield DevTools Broker", "/TR", taskCmd, "/SC", "ONLOGON", "/RL", "LIMITED", "/F")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
func startBrokerDetached(exe string) error {
	if _, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/health", brokerPort)); err == nil {
		return nil
	}
	cmd := exec.Command(exe, "broker")
	if runtime.GOOS == "windows" {
		cmd.SysProcAttr = windowsDetachedAttrs()
	}
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil
	return cmd.Start()
}

// Split out by build tags in platform files.
func windowsDetachedAttrs() *sysProcAttr { return detachedSysProcAttr() }

func waitBrokerHealth(timeout time.Duration) any {
	deadline := time.Now().Add(timeout)
	var last any
	for time.Now().Before(deadline) {
		last = fetchJSON(fmt.Sprintf("http://127.0.0.1:%d/health", brokerPort), 1200*time.Millisecond)
		if m, ok := last.(map[string]any); ok && m["ok"] == true {
			return last
		}
		time.Sleep(350 * time.Millisecond)
	}
	return last
}
func canonicalResultPaths() []string {
	var out []string
	out = append(out, filepath.Join(localAppData(), "DoubleTab", "MinefieldArtifactMesh", "MINEFIELD-ARTIFACT-MESH-V200__RESULT__LATEST.json"))
	downloads := filepath.Join(userProfile(), "Downloads")
	if downloads != "" {
		out = append(out, filepath.Join(downloads, "_DT_RUNS", "MINEFIELD-ARTIFACT-MESH-V200__RESULT__LATEST.json"), filepath.Join(downloads, "Minefield-ArtifactMesh-v2.0.1-RESULT.json"))
	}
	return out
}
func localAppData() string {
	if v := os.Getenv("LOCALAPPDATA"); v != "" {
		return v
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".local", "share")
	}
	return os.TempDir()
}
func userProfile() string {
	if v := os.Getenv("USERPROFILE"); v != "" {
		return v
	}
	h, _ := os.UserHomeDir()
	return h
}
func findCommand(names []string) string {
	for _, n := range names {
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
	}
	return ""
}

func findNode() string {
	if path := findCommand([]string{"node.exe", "node"}); path != "" {
		return path
	}
	for _, path := range []string{
		filepath.Join(os.Getenv("ProgramFiles"), "nodejs", "node.exe"),
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "nodejs", "node.exe"),
		filepath.Join(localAppData(), "Programs", "nodejs", "node.exe"),
	} {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	return ""
}

func findNpx() string {
	if path := findCommand([]string{"npx.cmd", "npx"}); path != "" {
		return path
	}
	for _, path := range []string{
		filepath.Join(os.Getenv("ProgramFiles"), "nodejs", "npx.cmd"),
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "nodejs", "npx.cmd"),
		filepath.Join(localAppData(), "Programs", "nodejs", "npx.cmd"),
	} {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	return ""
}
func samePath(a, b string) bool {
	aa, _ := filepath.Abs(a)
	bb, _ := filepath.Abs(b)
	return strings.EqualFold(filepath.Clean(aa), filepath.Clean(bb))
}
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err = os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}
func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err == nil {
		return nil
	}
	// Windows cannot always rename over an existing file. Preserve a rollback copy.
	old := path + ".replace-old"
	_ = os.Remove(old)
	if _, err := os.Stat(path); err == nil {
		if err := os.Rename(path, old); err != nil {
			_ = os.Remove(tmp)
			return err
		}
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Rename(old, path)
		return err
	}
	_ = os.Remove(old)
	return nil
}
func writeJSONAtomic(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(path, append(data, '\n'))
}
func printJSON(v any) { data, _ := json.MarshalIndent(v, "", "  "); fmt.Println(string(data)) }
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func zipDir(root, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(f)
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if rel == "." {
			return nil
		}
		name := filepath.ToSlash(rel)
		if d.IsDir() {
			_, err = zw.Create(name + "/")
			return err
		}
		info, _ := d.Info()
		hdr, _ := zip.FileInfoHeader(info)
		hdr.Name = name
		hdr.Method = zip.Deflate
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		_, err = io.Copy(w, in)
		return err
	})
	closeErr := zw.Close()
	fileErr := f.Close()
	if walkErr != nil {
		return walkErr
	}
	if closeErr != nil {
		return closeErr
	}
	return fileErr
}
func restoreZip(src, dest string) error {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, f := range zr.File {
		target := filepath.Join(dest, filepath.FromSlash(f.Name))
		cleanDest, _ := filepath.Abs(dest)
		cleanTarget, _ := filepath.Abs(target)
		if !strings.HasPrefix(cleanTarget, cleanDest+string(os.PathSeparator)) && cleanTarget != cleanDest {
			return errors.New("zip traversal")
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		r, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.Create(target)
		if err != nil {
			r.Close()
			return err
		}
		_, err = io.Copy(out, r)
		r.Close()
		out.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
func fetchJSON(raw string, timeout time.Duration) any {
	c := &http.Client{Timeout: timeout}
	resp, err := c.Get(raw)
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	defer resp.Body.Close()
	var v any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&v); err != nil {
		return map[string]any{"ok": false, "status": resp.StatusCode, "error": err.Error()}
	}
	if m, ok := v.(map[string]any); ok {
		if _, exists := m["ok"]; !exists {
			m["ok"] = resp.StatusCode >= 200 && resp.StatusCode < 300
		}
	}
	return v
}
func writeHTTPJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func corsLocal(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if strings.HasPrefix(origin, "chrome-extension://"+extensionID) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		} else if origin != "" && origin != "null" {
			writeHTTPJSON(w, 403, map[string]any{"ok": false, "error": "ORIGIN_DENIED"})
			return
		}
		w.Header().Set("Access-Control-Allow-Headers", "content-type, x-minefield-token")
		if r.Method == http.MethodOptions {
			w.WriteHeader(204)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
func intFromString(s string) int { i, _ := strconv.Atoi(s); return i }
func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
