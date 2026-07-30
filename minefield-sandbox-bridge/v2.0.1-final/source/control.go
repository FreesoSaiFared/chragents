package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const controlSchema = "minefield.total-control.config/1"

type ControlConfig struct {
	Schema                string             `json:"schema"`
	Version               string             `json:"version"`
	Token                 string             `json:"token"`
	Enabled               bool               `json:"enabled"`
	NeverUseExternalTasks bool               `json:"neverUseExternalTasks"`
	AuthorizedURLPrefixes []string           `json:"authorizedUrlPrefixes"`
	Shell                 ShellPolicy        `json:"shell"`
	Filesystem            FilesystemPolicy   `json:"filesystem"`
	Browser               BrowserPolicy      `json:"browser"`
	CUA                   CUAPolicy          `json:"cua"`
	Continuation          ContinuationPolicy `json:"continuation"`
	Artifacts             ArtifactPolicy     `json:"artifacts"`
	Locator               LocatorPolicy      `json:"locator"`
	Watchers              []WatchRule        `json:"watchers"`
	UpdatedAt             string             `json:"updatedAt"`
}

type ShellPolicy struct {
	Enabled             bool     `json:"enabled"`
	PowerShell          bool     `json:"powershell"`
	CMD                 bool     `json:"cmd"`
	DirectExec          bool     `json:"directExec"`
	Elevated            bool     `json:"elevated"`
	ElevationMode       string   `json:"elevationMode"`
	MaxTimeoutSeconds   int      `json:"maxTimeoutSeconds"`
	MaxOutputBytes      int      `json:"maxOutputBytes"`
	AllowedWorkingRoots []string `json:"allowedWorkingRoots"`
	DeniedPatterns      []string `json:"deniedPatterns"`
}

type FilesystemPolicy struct {
	Enabled      bool     `json:"enabled"`
	AllowedRoots []string `json:"allowedRoots"`
	Read         bool     `json:"read"`
	Write        bool     `json:"write"`
	Move         bool     `json:"move"`
	Copy         bool     `json:"copy"`
	Delete       bool     `json:"delete"`
	Watch        bool     `json:"watch"`
	MaxReadBytes int64    `json:"maxReadBytes"`
}

type BrowserPolicy struct {
	Enabled                bool     `json:"enabled"`
	RefreshTabs            bool     `json:"refreshTabs"`
	ReloadExtension        bool     `json:"reloadExtension"`
	RestartBrowser         bool     `json:"restartBrowser"`
	PreserveSession        bool     `json:"preserveSession"`
	ForceCloseAfterSeconds int      `json:"forceCloseAfterSeconds"`
	CDPPort                int      `json:"cdpPort"`
	ExecutableCandidates   []string `json:"executableCandidates"`
	ExtraLaunchArguments   []string `json:"extraLaunchArguments"`
}

type CUAPolicy struct {
	Enabled             bool     `json:"enabled"`
	AllowCoordinates    bool     `json:"allowCoordinates"`
	AllowSendKeys       bool     `json:"allowSendKeys"`
	AllowScreenshots    bool     `json:"allowScreenshots"`
	AllowedProcessNames []string `json:"allowedProcessNames"`
	MaxSteps            int      `json:"maxSteps"`
}

type ContinuationPolicy struct {
	Enabled               bool   `json:"enabled"`
	InternalOnly          bool   `json:"internalOnly"`
	Until                 string `json:"until"`
	IntervalSeconds       int    `json:"intervalSeconds"`
	ConversationURLPrefix string `json:"conversationUrlPrefix"`
	Prompt                string `json:"prompt"`
	PreserveDrafts        bool   `json:"preserveDrafts"`
	StopMarker            string `json:"stopMarker"`
}

type ArtifactPolicy struct {
	Enabled                  bool    `json:"enabled"`
	AutoDownloadZipLinks     bool    `json:"autoDownloadZipLinks"`
	DownloadsRoot            string  `json:"downloadsRoot"`
	DeleteZipAfterExtract    bool    `json:"deleteZipAfterExtract"`
	RejectNumberedDuplicates bool    `json:"rejectNumberedDuplicates"`
	RejectExistingDirectory  bool    `json:"rejectExistingDirectory"`
	ExactOriginReturn        bool    `json:"exactOriginReturn"`
	MaxArchiveBytes          int64   `json:"maxArchiveBytes"`
	MaxExpandedBytes         int64   `json:"maxExpandedBytes"`
	MaxEntries               int     `json:"maxEntries"`
	MaxCompressionRatio      float64 `json:"maxCompressionRatio"`
	QuarantineRoot           string  `json:"quarantineRoot"`
}

type LocatorPolicy struct {
	Enabled              bool     `json:"enabled"`
	MachineID            string   `json:"machineId"`
	BrowserInstanceID    string   `json:"browserInstanceId"`
	ProfileID            string   `json:"profileId"`
	ListenAddress        string   `json:"listenAddress"`
	AdvertiseURL         string   `json:"advertiseUrl"`
	PeerEndpoints        []string `json:"peerEndpoints"`
	PeerSharedSecret     string   `json:"peerSharedSecret"`
	AllowRemoteResolve   bool     `json:"allowRemoteResolve"`
	AllowRemoteRegister  bool     `json:"allowRemoteRegister"`
	AllowRemoteActuation bool     `json:"allowRemoteActuation"`
	LeaseSeconds         int      `json:"leaseSeconds"`
}

type WatchRule struct {
	ID            string         `json:"id"`
	Enabled       bool           `json:"enabled"`
	Root          string         `json:"root"`
	Glob          string         `json:"glob"`
	Recursive     bool           `json:"recursive"`
	StableSeconds int            `json:"stableSeconds"`
	Action        map[string]any `json:"action"`
}

type ControlPlane struct {
	root       string
	configPath string
	ledgerPath string
	mu         sync.RWMutex
	config     ControlConfig
	eventsMu   sync.Mutex
	events     []map[string]any
	seenMu     sync.Mutex
	seen       map[string]time.Time
	stop       chan struct{}
}

func defaultControlConfig() ControlConfig {
	local := localAppData()
	user := userProfile()
	downloads := filepath.Join(user, "Downloads")
	token := randomToken(32)
	return ControlConfig{
		Schema:                controlSchema,
		Version:               version,
		Token:                 token,
		Enabled:               true,
		NeverUseExternalTasks: true,
		AuthorizedURLPrefixes: []string{
			"https://chatgpt.com/g/g-p-6a5173aac2ec81919ad34808f3b9840d/",
			"https://chatgpt.com/apps/remote-desktop-commander/",
		},
		Shell: ShellPolicy{
			Enabled: true, PowerShell: true, CMD: true, DirectExec: true,
			Elevated: true, ElevationMode: "prompted-runas",
			MaxTimeoutSeconds: 900, MaxOutputBytes: 4 << 20,
			AllowedWorkingRoots: []string{user, `E:\`, `G:\My Drive\Doubletab`, local},
			DeniedPatterns: []string{
				`(?i)\bformat\s+[a-z]:`,
				`(?i)\bclear-disk\b`,
				`(?i)\bremove-item\s+[^\r\n]*-recurse[^\r\n]*\bwindows\b`,
				`(?i)\breg\s+delete\s+HKLM\\SYSTEM\b`,
			},
		},
		Filesystem: FilesystemPolicy{
			Enabled:      true,
			AllowedRoots: []string{user, `E:\`, `G:\My Drive\Doubletab`, local},
			Read:         true, Write: true, Move: true, Copy: true, Delete: false, Watch: true,
			MaxReadBytes: 16 << 20,
		},
		Browser: BrowserPolicy{
			Enabled: true, RefreshTabs: true, ReloadExtension: true, RestartBrowser: true,
			PreserveSession: true, ForceCloseAfterSeconds: 12, CDPPort: 9222,
			ExecutableCandidates: []string{
				filepath.Join(local, "Google", "Chrome SxS", "Application", "chrome.exe"),
				filepath.Join(local, "Google", "Chrome", "Application", "chrome.exe"),
				filepath.Join(os.Getenv("ProgramFiles"), "Google", "Chrome", "Application", "chrome.exe"),
			},
			ExtraLaunchArguments: []string{"--restore-last-session", "--remote-debugging-port=9222", "--remote-allow-origins=*"},
		},
		CUA: CUAPolicy{
			Enabled: true, AllowCoordinates: true, AllowSendKeys: true, AllowScreenshots: true,
			AllowedProcessNames: []string{"chrome", "chrome_canary", "powershell", "cmd", "explorer", "code", "notepad"},
			MaxSteps:            200,
		},
		Continuation: ContinuationPolicy{
			Enabled: true, InternalOnly: true,
			Until:                 "2026-07-30T12:00:00+02:00",
			IntervalSeconds:       60,
			ConversationURLPrefix: "https://chatgpt.com/g/g-p-6a5173aac2ec81919ad34808f3b9840d/c/6a68bdc1-7180-83eb-a990-521592229624",
			Prompt:                "Continue executing the current Minefield implementation autonomously in this same conversation. Do not create or use external scheduled tasks or task planning. Preserve human drafts and unrelated tabs. Implement, run, verify, repair, and continue until the configured cutoff. End only when no concrete work remains and emit [[MF:COMPLETE]].",
			PreserveDrafts:        true,
			StopMarker:            "[[MF:COMPLETE]]",
		},
		Artifacts: ArtifactPolicy{
			Enabled: true, AutoDownloadZipLinks: true, DownloadsRoot: downloads,
			DeleteZipAfterExtract: true, RejectNumberedDuplicates: true, RejectExistingDirectory: true,
			ExactOriginReturn: true, MaxArchiveBytes: 4 << 30, MaxExpandedBytes: 16 << 30,
			MaxEntries: 200000, MaxCompressionRatio: 250.0,
			QuarantineRoot: filepath.Join(downloads, "_MF_QUARANTINE"),
		},
		Locator: LocatorPolicy{
			Enabled: true, MachineID: stableMachineID(), BrowserInstanceID: "minefield-canary", ProfileID: "minefield",
			ListenAddress: "127.0.0.1:9791", AdvertiseURL: "", PeerEndpoints: []string{},
			PeerSharedSecret: randomToken(32), AllowRemoteResolve: true, AllowRemoteRegister: false,
			AllowRemoteActuation: false, LeaseSeconds: 120,
		},
		Watchers: []WatchRule{
			{
				ID: "all-zip-downloads", Enabled: true, Root: downloads, Glob: "*.zip", Recursive: false, StableSeconds: 3,
				Action: map[string]any{"kind": "artifact", "action": "ingest", "originUrl": ""},
			},
			{
				ID: "minefield-results", Enabled: true, Root: filepath.Join(downloads, "_DT_RUNS"), Glob: "*.json", Recursive: true, StableSeconds: 2,
				Action: map[string]any{"kind": "record", "label": "Minefield result detected"},
			},
		},
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
}

func ensureControlConfig() (ControlConfig, error) {
	root := filepath.Join(localAppData(), "DoubleTab", "MinefieldArtifactMesh")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return ControlConfig{}, err
	}
	path := filepath.Join(root, "capabilities.json")
	if data, err := os.ReadFile(path); err == nil {
		var cfg ControlConfig
		if json.Unmarshal(data, &cfg) == nil && cfg.Token != "" {
			cfg = normalizeControlConfig(cfg)
			_ = writeJSONAtomic(path, cfg)
			return cfg, nil
		}
	}
	cfg := defaultControlConfig()
	if err := writeJSONAtomic(path, cfg); err != nil {
		return ControlConfig{}, err
	}
	return cfg, nil
}

func normalizeControlConfig(cfg ControlConfig) ControlConfig {
	def := defaultControlConfig()
	if cfg.Schema == "" {
		cfg.Schema = def.Schema
	}
	cfg.Version = version
	if cfg.Token == "" {
		cfg.Token = def.Token
	}
	if cfg.Shell.MaxTimeoutSeconds <= 0 {
		cfg.Shell.MaxTimeoutSeconds = def.Shell.MaxTimeoutSeconds
	}
	if cfg.Shell.MaxOutputBytes <= 0 {
		cfg.Shell.MaxOutputBytes = def.Shell.MaxOutputBytes
	}
	if cfg.Filesystem.MaxReadBytes <= 0 {
		cfg.Filesystem.MaxReadBytes = def.Filesystem.MaxReadBytes
	}
	if cfg.Browser.CDPPort <= 0 {
		cfg.Browser.CDPPort = 9222
	}
	if cfg.CUA.MaxSteps <= 0 {
		cfg.CUA.MaxSteps = def.CUA.MaxSteps
	}
	if cfg.Continuation.IntervalSeconds < 15 {
		cfg.Continuation.IntervalSeconds = 60
	}
	if cfg.Artifacts.DownloadsRoot == "" {
		cfg.Artifacts.DownloadsRoot = def.Artifacts.DownloadsRoot
	}
	if cfg.Artifacts.QuarantineRoot == "" {
		cfg.Artifacts.QuarantineRoot = def.Artifacts.QuarantineRoot
	}
	if cfg.Artifacts.MaxArchiveBytes <= 0 {
		cfg.Artifacts.MaxArchiveBytes = def.Artifacts.MaxArchiveBytes
	}
	if cfg.Artifacts.MaxExpandedBytes <= 0 {
		cfg.Artifacts.MaxExpandedBytes = def.Artifacts.MaxExpandedBytes
	}
	if cfg.Artifacts.MaxEntries <= 0 {
		cfg.Artifacts.MaxEntries = def.Artifacts.MaxEntries
	}
	if cfg.Artifacts.MaxCompressionRatio <= 0 {
		cfg.Artifacts.MaxCompressionRatio = def.Artifacts.MaxCompressionRatio
	}
	if cfg.Locator.MachineID == "" {
		cfg.Locator.MachineID = stableMachineID()
	}
	if cfg.Locator.BrowserInstanceID == "" {
		cfg.Locator.BrowserInstanceID = def.Locator.BrowserInstanceID
	}
	if cfg.Locator.ProfileID == "" {
		cfg.Locator.ProfileID = def.Locator.ProfileID
	}
	if cfg.Locator.ListenAddress == "" {
		cfg.Locator.ListenAddress = def.Locator.ListenAddress
	}
	if cfg.Locator.PeerSharedSecret == "" {
		cfg.Locator.PeerSharedSecret = randomToken(32)
	}
	if cfg.Locator.LeaseSeconds <= 0 {
		cfg.Locator.LeaseSeconds = def.Locator.LeaseSeconds
	}
	cfg.NeverUseExternalTasks = true
	cfg.Continuation.InternalOnly = true
	cfg.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return cfg
}

func newControlPlane(root string) (*ControlPlane, error) {
	cfg, err := ensureControlConfig()
	if err != nil {
		return nil, err
	}
	controlRoot := filepath.Join(localAppData(), "DoubleTab", "MinefieldArtifactMesh")
	_ = os.MkdirAll(filepath.Join(controlRoot, "events"), 0o700)
	cp := &ControlPlane{
		root:       controlRoot,
		configPath: filepath.Join(controlRoot, "capabilities.json"),
		ledgerPath: filepath.Join(controlRoot, "watch-ledger.json"),
		config:     cfg,
		seen:       map[string]time.Time{},
		stop:       make(chan struct{}),
	}
	cp.loadSeen()
	_ = cp.ensureArtifactRoots()
	_ = cp.ensureLocatorRoot()
	if os.Getenv("MF_TEST_MODE") != "1" {
		go cp.watchLoop()
		go cp.startLocatorPeerServer()
	}
	cp.recordEvent("control.started", map[string]any{"version": version, "brokerRoot": root})
	return cp, nil
}

func (cp *ControlPlane) token() string {
	cp.mu.RLock()
	defer cp.mu.RUnlock()
	return cp.config.Token
}

func (cp *ControlPlane) status() map[string]any {
	cp.mu.RLock()
	cfg := cp.config
	cp.mu.RUnlock()
	cp.eventsMu.Lock()
	eventCount := len(cp.events)
	cp.eventsMu.Unlock()
	return map[string]any{
		"ok":                    true,
		"schema":                "minefield.total-control.status/1",
		"version":               version,
		"enabled":               cfg.Enabled,
		"neverUseExternalTasks": true,
		"configPath":            cp.configPath,
		"authorizedUrlPrefixes": cfg.AuthorizedURLPrefixes,
		"shell":                 cfg.Shell,
		"filesystem":            cfg.Filesystem,
		"browser":               cfg.Browser,
		"cua":                   cfg.CUA,
		"continuation":          cfg.Continuation,
		"artifacts":             cfg.Artifacts,
		"locator":               cfg.Locator,
		"watchers":              cfg.Watchers,
		"events":                eventCount,
	}
}

func (cp *ControlPlane) authorize(r *http.Request) error {
	if r.URL.Path == "/health" || r.URL.Path == "/control/status" {
		return nil
	}
	token := r.Header.Get("X-Minefield-Token")
	if token == "" {
		token = r.URL.Query().Get("token")
	}
	if token == "" || token != cp.token() {
		return errors.New("AUTH_TOKEN_REQUIRED")
	}
	return nil
}

func (cp *ControlPlane) authorizedOrigin(rawURL string) bool {
	cp.mu.RLock()
	prefixes := append([]string(nil), cp.config.AuthorizedURLPrefixes...)
	cp.mu.RUnlock()
	for _, p := range prefixes {
		if p != "" && strings.HasPrefix(rawURL, p) {
			return true
		}
	}
	return false
}

func (cp *ControlPlane) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeHTTPJSON(w, 200, cp.status())
}

func (cp *ControlPlane) handleConfig(w http.ResponseWriter, r *http.Request) {
	if err := cp.authorize(r); err != nil {
		writeHTTPJSON(w, 401, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	switch r.Method {
	case http.MethodGet:
		cp.mu.RLock()
		cfg := cp.config
		cp.mu.RUnlock()
		cfg.Token = "REDACTED"
		writeHTTPJSON(w, 200, map[string]any{"ok": true, "config": cfg})
	case http.MethodPost:
		var req struct {
			Config    ControlConfig `json:"config"`
			OriginURL string        `json:"originUrl"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(&req); err != nil {
			writeHTTPJSON(w, 400, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		if !cp.authorizedOrigin(req.OriginURL) {
			writeHTTPJSON(w, 403, map[string]any{"ok": false, "error": "ORIGIN_NOT_AUTHORIZED"})
			return
		}
		cp.mu.Lock()
		req.Config.Token = cp.config.Token
		req.Config = normalizeControlConfig(req.Config)
		cp.config = req.Config
		err := writeJSONAtomic(cp.configPath, cp.config)
		cp.mu.Unlock()
		if err != nil {
			writeHTTPJSON(w, 500, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		cp.recordEvent("config.updated", map[string]any{"originUrl": req.OriginURL})
		writeHTTPJSON(w, 200, map[string]any{"ok": true, "configPath": cp.configPath})
	default:
		writeHTTPJSON(w, 405, map[string]any{"ok": false, "error": "GET_OR_POST_REQUIRED"})
	}
}

type commandEnvelope struct {
	ID        string         `json:"id"`
	Kind      string         `json:"kind"`
	OriginURL string         `json:"originUrl"`
	TabID     int            `json:"tabId"`
	Payload   map[string]any `json:"payload"`
}

func (cp *ControlPlane) decodeCommand(r *http.Request) (commandEnvelope, error) {
	var env commandEnvelope
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<20)).Decode(&env); err != nil {
		return env, err
	}
	if env.ID == "" {
		env.ID = fmt.Sprintf("mf-%d", time.Now().UnixNano())
	}
	env.Kind = strings.ToLower(strings.TrimSpace(env.Kind))
	if !cp.authorizedOrigin(env.OriginURL) {
		return env, errors.New("ORIGIN_NOT_AUTHORIZED")
	}
	return env, nil
}

func (cp *ControlPlane) handleExecute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeHTTPJSON(w, 405, map[string]any{"ok": false, "error": "POST_REQUIRED"})
		return
	}
	if err := cp.authorize(r); err != nil {
		writeHTTPJSON(w, 401, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	env, err := cp.decodeCommand(r)
	if err != nil {
		writeHTTPJSON(w, 403, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	result := cp.executeEnvelope(env)
	status := 200
	if ok, _ := result["ok"].(bool); !ok {
		status = 400
	}
	writeHTTPJSON(w, status, result)
}

func (cp *ControlPlane) executeEnvelope(env commandEnvelope) map[string]any {
	cp.mu.RLock()
	cfg := cp.config
	cp.mu.RUnlock()
	if !cfg.Enabled {
		return map[string]any{"ok": false, "error": "CONTROL_DISABLED", "id": env.ID}
	}
	started := time.Now()
	var result map[string]any
	switch env.Kind {
	case "powershell", "cmd", "exec":
		result = cp.executeShell(env, cfg)
	case "fs", "filesystem":
		result = cp.executeFS(env, cfg)
	case "watch":
		result = cp.executeWatch(env, cfg)
	case "browser":
		result = cp.executeBrowser(env, cfg)
	case "cua":
		result = cp.executeCUA(env, cfg)
	case "continuation":
		result = cp.executeContinuation(env, cfg)
	case "artifact":
		result = cp.executeArtifact(env, cfg)
	case "locator":
		result = cp.executeLocator(env, cfg)
	case "record", "note":
		result = map[string]any{"ok": true, "recorded": env.Payload}
	default:
		result = map[string]any{"ok": false, "error": "UNKNOWN_KIND", "kind": env.Kind}
	}
	result["id"] = env.ID
	result["kind"] = env.Kind
	result["durationMs"] = time.Since(started).Milliseconds()
	result["at"] = time.Now().UTC().Format(time.RFC3339Nano)
	cp.recordEvent("command."+env.Kind, map[string]any{"envelope": env, "result": result})
	return result
}

func (cp *ControlPlane) executeShell(env commandEnvelope, cfg ControlConfig) map[string]any {
	p := env.Payload
	if !cfg.Shell.Enabled {
		return map[string]any{"ok": false, "error": "SHELL_DISABLED"}
	}
	shell := env.Kind
	if s := strings.ToLower(stringAny(p["shell"])); s != "" {
		shell = s
	}
	elevated := boolAny(p["elevated"])
	if elevated && !cfg.Shell.Elevated {
		return map[string]any{"ok": false, "error": "ELEVATION_DISABLED"}
	}
	command := stringAny(p["script"])
	if command == "" {
		command = stringAny(p["command"])
	}
	if command == "" {
		return map[string]any{"ok": false, "error": "EMPTY_COMMAND"}
	}
	for _, pattern := range cfg.Shell.DeniedPatterns {
		if re, err := regexp.Compile(pattern); err == nil && re.MatchString(command) {
			return map[string]any{"ok": false, "error": "COMMAND_DENIED_BY_PATTERN", "pattern": pattern}
		}
	}
	cwd := stringAny(p["cwd"])
	if cwd == "" {
		cwd = userProfile()
	}
	if !pathAllowed(cwd, cfg.Shell.AllowedWorkingRoots) {
		return map[string]any{"ok": false, "error": "WORKING_DIRECTORY_DENIED", "cwd": cwd}
	}
	timeoutSec := intAny(p["timeoutSeconds"])
	if timeoutSec <= 0 {
		timeoutSec = 120
	}
	if timeoutSec > cfg.Shell.MaxTimeoutSeconds {
		timeoutSec = cfg.Shell.MaxTimeoutSeconds
	}
	maxOut := cfg.Shell.MaxOutputBytes
	if maxOut <= 0 {
		maxOut = 4 << 20
	}

	if elevated {
		if cfg.Shell.ElevationMode == "preauthorized-task" {
			return cp.executeElevatedTask(shell, command, cwd, timeoutSec, maxOut)
		}
		return cp.executePromptedElevation(shell, command, cwd, timeoutSec)
	}

	var exe string
	var args []string
	switch shell {
	case "powershell", "pwsh":
		if !cfg.Shell.PowerShell {
			return map[string]any{"ok": false, "error": "POWERSHELL_DISABLED"}
		}
		exe = findCommand([]string{"pwsh.exe", "powershell.exe", "pwsh", "powershell"})
		if exe == "" {
			return map[string]any{"ok": false, "error": "POWERSHELL_NOT_FOUND"}
		}
		if strings.Contains(strings.ToLower(filepath.Base(exe)), "powershell") {
			args = []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", command}
		} else {
			args = []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command", command}
		}
	case "cmd":
		if !cfg.Shell.CMD {
			return map[string]any{"ok": false, "error": "CMD_DISABLED"}
		}
		exe = findCommand([]string{"cmd.exe", "cmd"})
		args = []string{"/D", "/S", "/C", command}
	case "exec":
		if !cfg.Shell.DirectExec {
			return map[string]any{"ok": false, "error": "DIRECT_EXEC_DISABLED"}
		}
		exe = stringAny(p["executable"])
		if exe == "" {
			return map[string]any{"ok": false, "error": "EXECUTABLE_REQUIRED"}
		}
		if !pathAllowed(exe, cfg.Shell.AllowedWorkingRoots) {
			return map[string]any{"ok": false, "error": "EXECUTABLE_PATH_DENIED", "executable": exe}
		}
		for _, a := range anyStringSlice(p["args"]) {
			args = append(args, a)
		}
	default:
		return map[string]any{"ok": false, "error": "SHELL_NOT_SUPPORTED", "shell": shell}
	}
	if exe == "" {
		return map[string]any{"ok": false, "error": "EXECUTABLE_NOT_FOUND"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Dir = cwd
	var stdout, stderr limitedBuffer
	stdout.limit, stderr.limit = maxOut, maxOut
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	started := time.Now()
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		exitCode = -1
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		}
	}
	out := map[string]any{
		"ok":         err == nil,
		"shell":      shell,
		"executable": exe,
		"cwd":        cwd,
		"exitCode":   exitCode,
		"stdout":     stdout.String(),
		"stderr":     stderr.String(),
		"timedOut":   ctx.Err() == context.DeadlineExceeded,
		"durationMs": time.Since(started).Milliseconds(),
		"truncated":  stdout.truncated || stderr.truncated,
	}
	if err != nil {
		out["error"] = err.Error()
	}
	return out
}

func (cp *ControlPlane) executePromptedElevation(shell, command, cwd string, timeoutSec int) map[string]any {
	if runtime.GOOS != "windows" {
		return map[string]any{"ok": false, "error": "ELEVATION_WINDOWS_ONLY"}
	}
	root := filepath.Join(cp.root, "elevated")
	_ = os.MkdirAll(root, 0o700)
	id := fmt.Sprintf("elevated-%d", time.Now().UnixNano())
	scriptPath := filepath.Join(root, id+".ps1")
	resultPath := filepath.Join(root, id+".result.json")
	payload := elevatedWorkerScript(shell, command, cwd, resultPath)
	if err := os.WriteFile(scriptPath, []byte(payload), 0o600); err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	ps := findCommand([]string{"powershell.exe", "powershell"})
	args := []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command",
		fmt.Sprintf("$p=Start-Process -FilePath %s -ArgumentList @('-NoLogo','-NoProfile','-ExecutionPolicy','Bypass','-File',%s) -Verb RunAs -PassThru -Wait; exit $p.ExitCode", psQuote(ps), psQuote(scriptPath))}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec+30)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ps, args...)
	output, err := cmd.CombinedOutput()
	result := map[string]any{"ok": false, "elevation": "prompted-runas", "scriptPath": scriptPath, "resultPath": resultPath, "launcherOutput": string(output)}
	if data, readErr := os.ReadFile(resultPath); readErr == nil {
		var parsed any
		if json.Unmarshal(data, &parsed) == nil {
			result["result"] = parsed
		}
	}
	if err == nil {
		result["ok"] = true
	} else {
		result["error"] = err.Error()
	}
	if ctx.Err() == context.DeadlineExceeded {
		result["timedOut"] = true
	}
	return result
}

func (cp *ControlPlane) executeElevatedTask(shell, command, cwd string, timeoutSec, maxOut int) map[string]any {
	queue := filepath.Join(cp.root, "elevated-queue")
	_ = os.MkdirAll(queue, 0o700)
	id := fmt.Sprintf("job-%d", time.Now().UnixNano())
	jobPath := filepath.Join(queue, id+".json")
	resultPath := filepath.Join(queue, id+".result.json")
	job := map[string]any{"id": id, "shell": shell, "command": command, "cwd": cwd, "resultPath": resultPath, "maxOutputBytes": maxOut}
	if err := writeJSONAtomic(jobPath, job); err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	schtasks := findCommand([]string{"schtasks.exe", "schtasks"})
	if schtasks == "" {
		return map[string]any{"ok": false, "error": "SCHTASKS_NOT_FOUND"}
	}
	taskName := "DoubleTab Minefield Elevated Executor"
	out, err := exec.Command(schtasks, "/Run", "/TN", taskName).CombinedOutput()
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error(), "output": string(out), "hint": "Run INSTALL-ELEVATED-HELPER.cmd once with UAC consent."}
	}
	deadline := time.Now().Add(time.Duration(timeoutSec+20) * time.Second)
	for time.Now().Before(deadline) {
		if data, readErr := os.ReadFile(resultPath); readErr == nil {
			var parsed any
			_ = json.Unmarshal(data, &parsed)
			return map[string]any{"ok": true, "elevation": "preauthorized-task", "result": parsed, "jobPath": jobPath}
		}
		time.Sleep(300 * time.Millisecond)
	}
	return map[string]any{"ok": false, "error": "ELEVATED_JOB_TIMEOUT", "jobPath": jobPath}
}

func elevatedWorkerScript(shell, command, cwd, resultPath string) string {
	return fmt.Sprintf(`$ErrorActionPreference='Continue'
$started=[DateTime]::UtcNow
try {
  Set-Location -LiteralPath %s
  $stdout=''; $stderr=''; $exit=0
  if (%s -eq 'cmd') {
    $tmpOut=[IO.Path]::GetTempFileName(); $tmpErr=[IO.Path]::GetTempFileName()
    $p=Start-Process -FilePath 'cmd.exe' -ArgumentList @('/D','/S','/C',%s) -Wait -PassThru -RedirectStandardOutput $tmpOut -RedirectStandardError $tmpErr
    $stdout=Get-Content -Raw -LiteralPath $tmpOut -ErrorAction SilentlyContinue; $stderr=Get-Content -Raw -LiteralPath $tmpErr -ErrorAction SilentlyContinue; $exit=$p.ExitCode
    Remove-Item $tmpOut,$tmpErr -Force -ErrorAction SilentlyContinue
  } else {
    $tmpOut=[IO.Path]::GetTempFileName(); $tmpErr=[IO.Path]::GetTempFileName()
    $p=Start-Process -FilePath 'powershell.exe' -ArgumentList @('-NoLogo','-NoProfile','-NonInteractive','-ExecutionPolicy','Bypass','-Command',%s) -Wait -PassThru -RedirectStandardOutput $tmpOut -RedirectStandardError $tmpErr
    $stdout=Get-Content -Raw -LiteralPath $tmpOut -ErrorAction SilentlyContinue; $stderr=Get-Content -Raw -LiteralPath $tmpErr -ErrorAction SilentlyContinue; $exit=$p.ExitCode
    Remove-Item $tmpOut,$tmpErr -Force -ErrorAction SilentlyContinue
  }
  $r=[ordered]@{ok=($exit -eq 0);exitCode=$exit;stdout=$stdout;stderr=$stderr;startedAt=$started.ToString('o');completedAt=[DateTime]::UtcNow.ToString('o')}
} catch { $r=[ordered]@{ok=$false;error=$_.Exception.ToString();startedAt=$started.ToString('o');completedAt=[DateTime]::UtcNow.ToString('o')} }
$r|ConvertTo-Json -Depth 8|Set-Content -Encoding UTF8 -LiteralPath %s
`, psQuote(cwd), psQuote(shell), psQuote(command), psQuote(command), psQuote(resultPath))
}

func (cp *ControlPlane) executeFS(env commandEnvelope, cfg ControlConfig) map[string]any {
	if !cfg.Filesystem.Enabled {
		return map[string]any{"ok": false, "error": "FILESYSTEM_DISABLED"}
	}
	p := env.Payload
	action := strings.ToLower(stringAny(p["action"]))
	path := filepath.Clean(stringAny(p["path"]))
	if path == "." || path == "" {
		return map[string]any{"ok": false, "error": "PATH_REQUIRED"}
	}
	if !pathAllowed(path, cfg.Filesystem.AllowedRoots) {
		return map[string]any{"ok": false, "error": "PATH_DENIED", "path": path}
	}
	switch action {
	case "list":
		if !cfg.Filesystem.Read {
			return map[string]any{"ok": false, "error": "READ_DISABLED"}
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			return map[string]any{"ok": false, "error": err.Error()}
		}
		limit := intAny(p["limit"])
		if limit <= 0 || limit > 5000 {
			limit = 500
		}
		out := make([]map[string]any, 0, minInt(len(entries), limit))
		for i, e := range entries {
			if i >= limit {
				break
			}
			info, _ := e.Info()
			item := map[string]any{"name": e.Name(), "path": filepath.Join(path, e.Name()), "dir": e.IsDir()}
			if info != nil {
				item["size"] = info.Size()
				item["modTime"] = info.ModTime().UTC().Format(time.RFC3339Nano)
			}
			out = append(out, item)
		}
		return map[string]any{"ok": true, "entries": out, "truncated": len(entries) > limit}
	case "stat":
		if !cfg.Filesystem.Read {
			return map[string]any{"ok": false, "error": "READ_DISABLED"}
		}
		info, err := os.Stat(path)
		if err != nil {
			return map[string]any{"ok": false, "error": err.Error()}
		}
		return map[string]any{"ok": true, "path": path, "name": info.Name(), "dir": info.IsDir(), "size": info.Size(), "mode": info.Mode().String(), "modTime": info.ModTime().UTC().Format(time.RFC3339Nano)}
	case "read":
		if !cfg.Filesystem.Read {
			return map[string]any{"ok": false, "error": "READ_DISABLED"}
		}
		max := cfg.Filesystem.MaxReadBytes
		if n := int64Any(p["maxBytes"]); n > 0 && n < max {
			max = n
		}
		f, err := os.Open(path)
		if err != nil {
			return map[string]any{"ok": false, "error": err.Error()}
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, max+1))
		if err != nil {
			return map[string]any{"ok": false, "error": err.Error()}
		}
		truncated := int64(len(data)) > max
		if truncated {
			data = data[:max]
		}
		return map[string]any{"ok": true, "path": path, "text": string(data), "bytes": len(data), "truncated": truncated}
	case "write":
		if !cfg.Filesystem.Write {
			return map[string]any{"ok": false, "error": "WRITE_DISABLED"}
		}
		data := []byte(stringAny(p["text"]))
		if b64 := stringAny(p["base64"]); b64 != "" {
			return map[string]any{"ok": false, "error": "BASE64_WRITE_NOT_ENABLED"}
		}
		if boolAny(p["append"]) {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return map[string]any{"ok": false, "error": err.Error()}
			}
			f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
			if err != nil {
				return map[string]any{"ok": false, "error": err.Error()}
			}
			_, err = f.Write(data)
			_ = f.Close()
			if err != nil {
				return map[string]any{"ok": false, "error": err.Error()}
			}
		} else {
			if err := writeAtomic(path, data); err != nil {
				return map[string]any{"ok": false, "error": err.Error()}
			}
		}
		h := sha256.Sum256(data)
		return map[string]any{"ok": true, "path": path, "bytes": len(data), "sha256": hex.EncodeToString(h[:])}
	case "mkdir":
		if !cfg.Filesystem.Write {
			return map[string]any{"ok": false, "error": "WRITE_DISABLED"}
		}
		if err := os.MkdirAll(path, 0o755); err != nil {
			return map[string]any{"ok": false, "error": err.Error()}
		}
		return map[string]any{"ok": true, "path": path}
	case "copy":
		if !cfg.Filesystem.Copy {
			return map[string]any{"ok": false, "error": "COPY_DISABLED"}
		}
		dest := filepath.Clean(stringAny(p["destination"]))
		if !pathAllowed(dest, cfg.Filesystem.AllowedRoots) {
			return map[string]any{"ok": false, "error": "DESTINATION_DENIED"}
		}
		if err := copyPath(path, dest); err != nil {
			return map[string]any{"ok": false, "error": err.Error()}
		}
		return map[string]any{"ok": true, "source": path, "destination": dest}
	case "move":
		if !cfg.Filesystem.Move {
			return map[string]any{"ok": false, "error": "MOVE_DISABLED"}
		}
		dest := filepath.Clean(stringAny(p["destination"]))
		if !pathAllowed(dest, cfg.Filesystem.AllowedRoots) {
			return map[string]any{"ok": false, "error": "DESTINATION_DENIED"}
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return map[string]any{"ok": false, "error": err.Error()}
		}
		if err := os.Rename(path, dest); err != nil {
			return map[string]any{"ok": false, "error": err.Error()}
		}
		return map[string]any{"ok": true, "source": path, "destination": dest}
	case "delete":
		if !cfg.Filesystem.Delete {
			return map[string]any{"ok": false, "error": "DELETE_DISABLED"}
		}
		if boolAny(p["recursive"]) {
			if err := os.RemoveAll(path); err != nil {
				return map[string]any{"ok": false, "error": err.Error()}
			}
		} else {
			if err := os.Remove(path); err != nil {
				return map[string]any{"ok": false, "error": err.Error()}
			}
		}
		return map[string]any{"ok": true, "path": path}
	case "hash":
		if !cfg.Filesystem.Read {
			return map[string]any{"ok": false, "error": "READ_DISABLED"}
		}
		h, err := fileSHA256(path)
		if err != nil {
			return map[string]any{"ok": false, "error": err.Error()}
		}
		return map[string]any{"ok": true, "path": path, "sha256": h}
	default:
		return map[string]any{"ok": false, "error": "FS_ACTION_NOT_SUPPORTED", "action": action}
	}
}

func (cp *ControlPlane) executeWatch(env commandEnvelope, cfg ControlConfig) map[string]any {
	if !cfg.Filesystem.Watch {
		return map[string]any{"ok": false, "error": "WATCH_DISABLED"}
	}
	action := strings.ToLower(stringAny(env.Payload["action"]))
	switch action {
	case "list", "status":
		return map[string]any{"ok": true, "watchers": cfg.Watchers}
	case "add", "update":
		data, _ := json.Marshal(env.Payload["rule"])
		var rule WatchRule
		if json.Unmarshal(data, &rule) != nil || rule.ID == "" {
			return map[string]any{"ok": false, "error": "INVALID_RULE"}
		}
		if !pathAllowed(rule.Root, cfg.Filesystem.AllowedRoots) {
			return map[string]any{"ok": false, "error": "WATCH_ROOT_DENIED"}
		}
		cp.mu.Lock()
		found := false
		for i, r := range cp.config.Watchers {
			if r.ID == rule.ID {
				cp.config.Watchers[i] = rule
				found = true
			}
		}
		if !found {
			cp.config.Watchers = append(cp.config.Watchers, rule)
		}
		cp.config.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		err := writeJSONAtomic(cp.configPath, cp.config)
		cp.mu.Unlock()
		if err != nil {
			return map[string]any{"ok": false, "error": err.Error()}
		}
		return map[string]any{"ok": true, "rule": rule}
	case "remove":
		id := stringAny(env.Payload["id"])
		cp.mu.Lock()
		var next []WatchRule
		for _, r := range cp.config.Watchers {
			if r.ID != id {
				next = append(next, r)
			}
		}
		cp.config.Watchers = next
		err := writeJSONAtomic(cp.configPath, cp.config)
		cp.mu.Unlock()
		if err != nil {
			return map[string]any{"ok": false, "error": err.Error()}
		}
		return map[string]any{"ok": true, "removed": id}
	default:
		return map[string]any{"ok": false, "error": "WATCH_ACTION_NOT_SUPPORTED"}
	}
}

func (cp *ControlPlane) executeBrowser(env commandEnvelope, cfg ControlConfig) map[string]any {
	if !cfg.Browser.Enabled {
		return map[string]any{"ok": false, "error": "BROWSER_CONTROL_DISABLED"}
	}
	action := strings.ToLower(stringAny(env.Payload["action"]))
	switch action {
	case "restart", "restart-browser":
		if !cfg.Browser.RestartBrowser {
			return map[string]any{"ok": false, "error": "BROWSER_RESTART_DISABLED"}
		}
		return restartChrome(cfg.Browser)
	case "start", "start-browser":
		return startChrome(cfg.Browser)
	case "cdp-status":
		return map[string]any{"ok": true, "cdp": fetchJSON(fmt.Sprintf("http://127.0.0.1:%d/json/version", cfg.Browser.CDPPort), 2*time.Second)}
	default:
		return map[string]any{"ok": false, "error": "BROWSER_ACTION_HANDLED_BY_EXTENSION_OR_UNSUPPORTED", "action": action}
	}
}

func restartChrome(policy BrowserPolicy) map[string]any {
	exe := findChromeExecutable(policy.ExecutableCandidates)
	if exe == "" {
		return map[string]any{"ok": false, "error": "CHROME_EXECUTABLE_NOT_FOUND"}
	}
	processName := strings.TrimSuffix(filepath.Base(exe), filepath.Ext(exe))
	before := listChromeProcesses(exe)
	policy.ExtraLaunchArguments = mergeUniqueArgs(policy.ExtraLaunchArguments, extractChromePreservedArgs(before))
	if runtime.GOOS == "windows" {
		ps := findCommand([]string{"powershell.exe", "powershell"})
		script := fmt.Sprintf(`Get-CimInstance Win32_Process | Where-Object { $_.ExecutablePath -and $_.ExecutablePath -ieq %s } | ForEach-Object { try { Stop-Process -Id $_.ProcessId -ErrorAction Stop } catch {} }`, psQuote(exe))
		_ = exec.Command(ps, "-NoProfile", "-NonInteractive", "-Command", script).Run()
		deadline := time.Now().Add(time.Duration(policy.ForceCloseAfterSeconds) * time.Second)
		for time.Now().Before(deadline) {
			if chromeProcessCount(exe) == 0 {
				break
			}
			time.Sleep(300 * time.Millisecond)
		}
		if chromeProcessCount(exe) > 0 {
			_ = exec.Command("taskkill.exe", "/F", "/IM", filepath.Base(exe), "/T").Run()
		}
	} else {
		_ = exec.Command("pkill", "-f", exe).Run()
		time.Sleep(time.Second)
	}
	started := startChrome(policy)
	started["processName"] = processName
	started["before"] = before
	started["restart"] = true
	return started
}

func startChrome(policy BrowserPolicy) map[string]any {
	exe := findChromeExecutable(policy.ExecutableCandidates)
	if exe == "" {
		return map[string]any{"ok": false, "error": "CHROME_EXECUTABLE_NOT_FOUND"}
	}
	args := append([]string(nil), policy.ExtraLaunchArguments...)
	if policy.PreserveSession && !containsString(args, "--restore-last-session") {
		args = append(args, "--restore-last-session")
	}
	if !containsPrefix(args, "--remote-debugging-port=") {
		args = append(args, fmt.Sprintf("--remote-debugging-port=%d", policy.CDPPort))
	}
	cmd := exec.Command(exe, args...)
	if runtime.GOOS == "windows" {
		cmd.SysProcAttr = windowsDetachedAttrs()
	}
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil
	err := cmd.Start()
	out := map[string]any{"ok": err == nil, "executable": exe, "args": args}
	if err != nil {
		out["error"] = err.Error()
		return out
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		h := fetchJSON(fmt.Sprintf("http://127.0.0.1:%d/json/version", policy.CDPPort), 1200*time.Millisecond)
		if m, ok := h.(map[string]any); ok && m["ok"] != false {
			out["cdp"] = h
			out["cdpReady"] = true
			return out
		}
		time.Sleep(500 * time.Millisecond)
	}
	out["cdpReady"] = false
	return out
}

func (cp *ControlPlane) executeCUA(env commandEnvelope, cfg ControlConfig) map[string]any {
	if !cfg.CUA.Enabled {
		return map[string]any{"ok": false, "error": "CUA_DISABLED"}
	}
	rawSteps, _ := env.Payload["steps"].([]any)
	if len(rawSteps) == 0 {
		return map[string]any{"ok": false, "error": "CUA_STEPS_REQUIRED"}
	}
	if len(rawSteps) > cfg.CUA.MaxSteps {
		return map[string]any{"ok": false, "error": "CUA_STEP_LIMIT"}
	}
	script, err := buildCUAScript(rawSteps, cfg.CUA, cp.root)
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	shellEnv := commandEnvelope{ID: env.ID + "-cua", Kind: "powershell", OriginURL: env.OriginURL, Payload: map[string]any{"script": script, "cwd": cp.root, "timeoutSeconds": intAny(env.Payload["timeoutSeconds"])}}
	result := cp.executeShell(shellEnv, cfg)
	result["cuaSteps"] = len(rawSteps)
	return result
}

func buildCUAScript(steps []any, policy CUAPolicy, root string) (string, error) {
	var b strings.Builder
	b.WriteString(`$ErrorActionPreference='Stop'
Add-Type @"
using System;
using System.Runtime.InteropServices;
public static class MFUser32 {
 [DllImport("user32.dll")] public static extern bool SetForegroundWindow(IntPtr hWnd);
 [DllImport("user32.dll")] public static extern bool ShowWindowAsync(IntPtr hWnd,int nCmdShow);
 [DllImport("user32.dll")] public static extern bool SetCursorPos(int X,int Y);
 [DllImport("user32.dll")] public static extern void mouse_event(uint flags,uint dx,uint dy,uint data,UIntPtr extra);
}
"@
Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing
$results=@()
`)
	for i, raw := range steps {
		data, _ := json.Marshal(raw)
		var step map[string]any
		if json.Unmarshal(data, &step) != nil {
			return "", fmt.Errorf("invalid step %d", i)
		}
		action := strings.ToLower(stringAny(step["action"]))
		switch action {
		case "wait":
			ms := intAny(step["ms"])
			if ms < 0 || ms > 600000 {
				return "", fmt.Errorf("invalid wait at step %d", i)
			}
			b.WriteString(fmt.Sprintf("Start-Sleep -Milliseconds %d\n$results += @{step=%d;action='wait';ok=$true}\n", ms, i))
		case "focus_window":
			proc := stringAny(step["process"])
			if !allowedName(proc, policy.AllowedProcessNames) {
				return "", fmt.Errorf("process denied at step %d", i)
			}
			b.WriteString(fmt.Sprintf("$p=Get-Process -Name %s -ErrorAction SilentlyContinue|Where-Object {$_.MainWindowHandle -ne 0}|Select-Object -First 1;if(!$p){throw 'WINDOW_NOT_FOUND'};[MFUser32]::ShowWindowAsync($p.MainWindowHandle,9)|Out-Null;[MFUser32]::SetForegroundWindow($p.MainWindowHandle)|Out-Null;$results += @{step=%d;action='focus_window';ok=$true;pid=$p.Id}\n", psQuote(proc), i))
		case "send_keys":
			if !policy.AllowSendKeys {
				return "", errors.New("send_keys disabled")
			}
			keys := stringAny(step["keys"])
			b.WriteString(fmt.Sprintf("[System.Windows.Forms.SendKeys]::SendWait(%s);$results += @{step=%d;action='send_keys';ok=$true}\n", psQuote(keys), i))
		case "click":
			if !policy.AllowCoordinates {
				return "", errors.New("coordinate clicks disabled")
			}
			x := intAny(step["x"])
			y := intAny(step["y"])
			b.WriteString(fmt.Sprintf("[MFUser32]::SetCursorPos(%d,%d)|Out-Null;[MFUser32]::mouse_event(2,0,0,0,[UIntPtr]::Zero);[MFUser32]::mouse_event(4,0,0,0,[UIntPtr]::Zero);$results += @{step=%d;action='click';ok=$true;x=%d;y=%d}\n", x, y, i, x, y))
		case "screenshot":
			if !policy.AllowScreenshots {
				return "", errors.New("screenshots disabled")
			}
			path := stringAny(step["path"])
			if path == "" {
				path = filepath.Join(root, fmt.Sprintf("screenshot-%d.png", time.Now().UnixNano()))
			}
			b.WriteString(fmt.Sprintf("$bounds=[System.Windows.Forms.Screen]::PrimaryScreen.Bounds;$bmp=New-Object Drawing.Bitmap $bounds.Width,$bounds.Height;$g=[Drawing.Graphics]::FromImage($bmp);$g.CopyFromScreen($bounds.Location,[Drawing.Point]::Empty,$bounds.Size);$bmp.Save(%s,[Drawing.Imaging.ImageFormat]::Png);$g.Dispose();$bmp.Dispose();$results += @{step=%d;action='screenshot';ok=$true;path=%s}\n", psQuote(path), i, psQuote(path)))
		default:
			return "", fmt.Errorf("unsupported CUA action %q at step %d", action, i)
		}
	}
	b.WriteString("$results|ConvertTo-Json -Depth 8")
	return b.String(), nil
}

func (cp *ControlPlane) executeContinuation(env commandEnvelope, cfg ControlConfig) map[string]any {
	action := strings.ToLower(stringAny(env.Payload["action"]))
	if action == "" {
		action = "set"
	}
	cp.mu.Lock()
	defer cp.mu.Unlock()
	switch action {
	case "set", "enable":
		c := cp.config.Continuation
		if v := stringAny(env.Payload["until"]); v != "" {
			c.Until = v
		}
		if v := intAny(env.Payload["intervalSeconds"]); v >= 15 {
			c.IntervalSeconds = v
		}
		if v := stringAny(env.Payload["conversationUrlPrefix"]); v != "" {
			c.ConversationURLPrefix = v
		}
		if v := stringAny(env.Payload["prompt"]); v != "" {
			c.Prompt = v
		}
		c.Enabled = true
		c.InternalOnly = true
		cp.config.NeverUseExternalTasks = true
		cp.config.Continuation = c
	case "disable", "stop":
		cp.config.Continuation.Enabled = false
	case "status":
		return map[string]any{"ok": true, "continuation": cp.config.Continuation, "neverUseExternalTasks": true}
	default:
		return map[string]any{"ok": false, "error": "CONTINUATION_ACTION_NOT_SUPPORTED"}
	}
	cp.config.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := writeJSONAtomic(cp.configPath, cp.config); err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	return map[string]any{"ok": true, "continuation": cp.config.Continuation, "neverUseExternalTasks": true}
}

func (cp *ControlPlane) handleEvents(w http.ResponseWriter, r *http.Request) {
	if err := cp.authorize(r); err != nil {
		writeHTTPJSON(w, 401, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	cp.eventsMu.Lock()
	events := append([]map[string]any(nil), cp.events...)
	cp.eventsMu.Unlock()
	limit := intAnyString(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	if len(events) > limit {
		events = events[len(events)-limit:]
	}
	writeHTTPJSON(w, 200, map[string]any{"ok": true, "events": events})
}

func (cp *ControlPlane) recordEvent(kind string, data map[string]any) {
	event := map[string]any{"kind": kind, "at": time.Now().UTC().Format(time.RFC3339Nano), "data": data}
	cp.eventsMu.Lock()
	cp.events = append(cp.events, event)
	if len(cp.events) > 2000 {
		cp.events = cp.events[len(cp.events)-2000:]
	}
	cp.eventsMu.Unlock()
	path := filepath.Join(cp.root, "events", fmt.Sprintf("%s-%d.json", sanitizeFilename(kind), time.Now().UnixNano()))
	_ = writeJSONAtomic(path, event)
	_ = writeJSONAtomic(filepath.Join(cp.root, "MINEFIELD-ARTIFACT-MESH__EVENT__LATEST.json"), event)
}

func (cp *ControlPlane) watchLoop() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-cp.stop:
			return
		case <-ticker.C:
			cp.scanWatchers()
		}
	}
}
func (cp *ControlPlane) scanWatchers() {
	cp.mu.RLock()
	cfg := cp.config
	rules := append([]WatchRule(nil), cfg.Watchers...)
	cp.mu.RUnlock()
	if !cfg.Filesystem.Watch {
		return
	}
	for _, rule := range rules {
		if !rule.Enabled || rule.Root == "" {
			continue
		}
		_ = cp.scanRule(rule, cfg)
	}
}
func (cp *ControlPlane) scanRule(rule WatchRule, cfg ControlConfig) error {
	root := filepath.Clean(rule.Root)
	if !pathAllowed(root, cfg.Filesystem.AllowedRoots) {
		return nil
	}
	glob := rule.Glob
	if glob == "" {
		glob = "*"
	}
	stable := time.Duration(maxInt(rule.StableSeconds, 1)) * time.Second
	now := time.Now()
	visit := func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != root && !rule.Recursive {
				return filepath.SkipDir
			}
			return nil
		}
		match, _ := filepath.Match(glob, d.Name())
		if !match {
			return nil
		}
		info, err := d.Info()
		if err != nil || now.Sub(info.ModTime()) < stable {
			return nil
		}
		key := rule.ID + "|" + strings.ToLower(path) + fmt.Sprintf("|%d|%d", info.Size(), info.ModTime().UnixNano())
		cp.seenMu.Lock()
		_, seen := cp.seen[key]
		if !seen {
			cp.seen[key] = now
		}
		cp.seenMu.Unlock()
		if seen {
			return nil
		}
		cp.saveSeen()
		event := map[string]any{"watchId": rule.ID, "path": path, "size": info.Size(), "modTime": info.ModTime().UTC().Format(time.RFC3339Nano), "action": rule.Action}
		cp.recordEvent("watch.detected", event)
		kind := strings.ToLower(stringAny(rule.Action["kind"]))
		if kind != "" && kind != "record" {
			origin := ""
			if len(cfg.AuthorizedURLPrefixes) > 0 {
				origin = cfg.AuthorizedURLPrefixes[0]
			}
			payload := map[string]any{}
			if raw, ok := rule.Action["payload"].(map[string]any); ok {
				for k, v := range raw {
					payload[k] = v
				}
			}
			payload["detectedPath"] = path
			res := cp.executeEnvelope(commandEnvelope{ID: "watch-" + rule.ID + fmt.Sprintf("-%d", time.Now().UnixNano()), Kind: kind, OriginURL: origin, Payload: payload})
			cp.recordEvent("watch.action", map[string]any{"watchId": rule.ID, "path": path, "result": res})
		}
		return nil
	}
	if rule.Recursive {
		return filepath.WalkDir(root, visit)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, e := range entries {
		_ = visit(filepath.Join(root, e.Name()), e, nil)
	}
	return nil
}
func (cp *ControlPlane) loadSeen() {
	data, err := os.ReadFile(cp.ledgerPath)
	if err != nil {
		return
	}
	var raw map[string]string
	if json.Unmarshal(data, &raw) != nil {
		return
	}
	for k, v := range raw {
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			cp.seen[k] = t
		}
	}
}
func (cp *ControlPlane) saveSeen() {
	cp.seenMu.Lock()
	raw := map[string]string{}
	cutoff := time.Now().Add(-30 * 24 * time.Hour)
	for k, v := range cp.seen {
		if v.After(cutoff) {
			raw[k] = v.UTC().Format(time.RFC3339Nano)
		}
	}
	cp.seenMu.Unlock()
	_ = writeJSONAtomic(cp.ledgerPath, raw)
}

func installPersistenceFallback(exe string) []string {
	var warnings []string
	if runtime.GOOS != "windows" {
		return warnings
	}
	reg := findCommand([]string{"reg.exe", "reg"})
	if reg != "" {
		value := fmt.Sprintf("\"%s\" broker", exe)
		out, err := exec.Command(reg, "ADD", `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`, "/V", "DoubleTabMinefieldArtifactMesh", "/T", "REG_SZ", "/D", value, "/F").CombinedOutput()
		if err != nil {
			warnings = append(warnings, "HKCU Run: "+err.Error()+": "+string(out))
		}
	}
	startup := filepath.Join(os.Getenv("APPDATA"), "Microsoft", "Windows", "Start Menu", "Programs", "Startup")
	if startup != "" {
		_ = os.MkdirAll(startup, 0o755)
		cmd := filepath.Join(startup, "DoubleTab-Minefield-ArtifactMesh.cmd")
		body := fmt.Sprintf("@echo off\r\nstart \"\" /min \"%s\" broker\r\n", exe)
		if err := os.WriteFile(cmd, []byte(body), 0o644); err != nil {
			warnings = append(warnings, "Startup folder: "+err.Error())
		}
	}
	return warnings
}

func installElevatedHelper(exe string) error {
	if runtime.GOOS != "windows" {
		return errors.New("Windows only")
	}
	schtasks := findCommand([]string{"schtasks.exe", "schtasks"})
	if schtasks == "" {
		return errors.New("schtasks not found")
	}
	taskCmd := fmt.Sprintf("\"%s\" elevated-worker", exe)
	out, err := exec.Command(schtasks, "/Create", "/TN", "DoubleTab Minefield Elevated Executor", "/TR", taskCmd, "/SC", "ONDEMAND", "/RL", "HIGHEST", "/F").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func runElevatedWorker() error {
	root := filepath.Join(localAppData(), "DoubleTab", "MinefieldArtifactMesh", "elevated-queue")
	_ = os.MkdirAll(root, 0o700)
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || strings.HasSuffix(e.Name(), ".result.json") {
			continue
		}
		path := filepath.Join(root, e.Name())
		var job map[string]any
		data, err := os.ReadFile(path)
		if err != nil || json.Unmarshal(data, &job) != nil {
			continue
		}
		shell := strings.ToLower(stringAny(job["shell"]))
		command := stringAny(job["command"])
		cwd := stringAny(job["cwd"])
		resultPath := stringAny(job["resultPath"])
		var exe string
		var args []string
		if shell == "cmd" {
			exe = "cmd.exe"
			args = []string{"/D", "/S", "/C", command}
		} else {
			exe = "powershell.exe"
			args = []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", command}
		}
		cmd := exec.Command(exe, args...)
		cmd.Dir = cwd
		var out, errout bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &errout
		runErr := cmd.Run()
		exit := 0
		if runErr != nil {
			exit = -1
			if ee, ok := runErr.(*exec.ExitError); ok {
				exit = ee.ExitCode()
			}
		}
		res := map[string]any{"ok": runErr == nil, "exitCode": exit, "stdout": out.String(), "stderr": errout.String(), "completedAt": time.Now().UTC().Format(time.RFC3339Nano)}
		if runErr != nil {
			res["error"] = runErr.Error()
		}
		_ = writeJSONAtomic(resultPath, res)
		_ = os.Rename(path, path+".done")
	}
	return nil
}

func renderBackgroundWithToken(src []byte) []byte {
	cfg, err := ensureControlConfig()
	if err != nil {
		return src
	}
	return bytes.ReplaceAll(src, []byte("__MF_BROKER_TOKEN__"), []byte(cfg.Token))
}

func copyPath(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return copyFile(src, dst)
	}
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target)
	})
}
func pathAllowed(path string, roots []string) bool {
	if path == "" {
		return false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	abs = strings.ToLower(filepath.Clean(abs))
	for _, root := range roots {
		if root == "" {
			continue
		}
		r, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		r = strings.ToLower(filepath.Clean(r))
		if abs == r || strings.HasPrefix(abs, r+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}
func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
func stringAny(v any) string {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		return string(x)
	case json.Number:
		return x.String()
	case fmt.Stringer:
		return x.String()
	default:
		return fmt.Sprint(x)
	}
}

func boolAny(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return strings.EqualFold(x, "true") || x == "1"
	case float64:
		return x != 0
	}
	return false
}
func intAny(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	case int64:
		return int(x)
	case string:
		return intAnyString(x)
	case json.Number:
		i, _ := x.Int64()
		return int(i)
	}
	return 0
}
func int64Any(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case int64:
		return x
	case int:
		return int64(x)
	case string:
		i, _ := time.ParseDuration(x)
		return int64(i)
	case json.Number:
		i, _ := x.Int64()
		return i
	}
	return 0
}
func intAnyString(s string) int { var n int; fmt.Sscanf(s, "%d", &n); return n }
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func containsString(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
func containsPrefix(xs []string, p string) bool {
	for _, x := range xs {
		if strings.HasPrefix(x, p) {
			return true
		}
	}
	return false
}
func allowedName(name string, allowed []string) bool {
	name = strings.ToLower(strings.TrimSuffix(filepath.Base(name), filepath.Ext(name)))
	for _, a := range allowed {
		if name == strings.ToLower(a) {
			return true
		}
	}
	return false
}
func findChromeExecutable(candidates []string) string {
	for _, p := range candidates {
		if p != "" {
			if info, err := os.Stat(p); err == nil && !info.IsDir() {
				return p
			}
		}
	}
	return findCommand([]string{"chrome.exe", "chrome", "google-chrome", "chromium"})
}

func mergeUniqueArgs(base []string, extra []string) []string {
	out := append([]string(nil), base...)
	seen := map[string]bool{}
	for _, arg := range out {
		key := strings.ToLower(strings.SplitN(arg, "=", 2)[0])
		seen[key] = true
	}
	for _, arg := range extra {
		key := strings.ToLower(strings.SplitN(arg, "=", 2)[0])
		if arg != "" && !seen[key] {
			seen[key] = true
			out = append(out, arg)
		}
	}
	return out
}

func extractChromePreservedArgs(processes any) []string {
	var commandLines []string
	switch v := processes.(type) {
	case map[string]any:
		if line := stringAny(v["CommandLine"]); line != "" {
			commandLines = append(commandLines, line)
		}
	case []any:
		for _, raw := range v {
			if item, ok := raw.(map[string]any); ok {
				if line := stringAny(item["CommandLine"]); line != "" {
					commandLines = append(commandLines, line)
				}
			}
		}
	case string:
		commandLines = append(commandLines, v)
	}
	wanted := []string{"--user-data-dir", "--profile-directory", "--load-extension", "--disable-extensions-except", "--remote-debugging-port", "--remote-allow-origins"}
	for _, line := range commandLines {
		tokens := splitWindowsCommandLine(line)
		var out []string
		for i := 1; i < len(tokens); i++ {
			token := tokens[i]
			lower := strings.ToLower(token)
			for _, prefix := range wanted {
				if lower == prefix && i+1 < len(tokens) {
					out = append(out, token+"="+tokens[i+1])
					i++
					break
				}
				if strings.HasPrefix(lower, prefix+"=") {
					out = append(out, token)
					break
				}
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return nil
}

func splitWindowsCommandLine(s string) []string {
	var out []string
	var b strings.Builder
	quoted := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			quoted = !quoted
		case ' ', '\t':
			if quoted {
				b.WriteByte(c)
			} else if b.Len() > 0 {
				out = append(out, b.String())
				b.Reset()
			}
		default:
			b.WriteByte(c)
		}
	}
	if b.Len() > 0 {
		out = append(out, b.String())
	}
	return out
}

func chromeProcessCount(exe string) int {
	if runtime.GOOS != "windows" {
		return 0
	}
	ps := findCommand([]string{"powershell.exe", "powershell"})
	if ps == "" {
		return 0
	}
	script := fmt.Sprintf(`@(Get-CimInstance Win32_Process | Where-Object { $_.ExecutablePath -and $_.ExecutablePath -ieq %s }).Count`, psQuote(exe))
	out, err := exec.Command(ps, "-NoProfile", "-NonInteractive", "-Command", script).CombinedOutput()
	if err != nil {
		return 0
	}
	return intAnyString(strings.TrimSpace(string(out)))
}

func listChromeProcesses(exe string) any {
	if runtime.GOOS != "windows" {
		return []any{}
	}
	ps := findCommand([]string{"powershell.exe", "powershell"})
	if ps == "" {
		return []any{}
	}
	script := fmt.Sprintf(`Get-CimInstance Win32_Process | Where-Object { $_.ExecutablePath -and $_.ExecutablePath -ieq %s } | Select-Object ProcessId,ExecutablePath,CommandLine | ConvertTo-Json -Depth 4`, psQuote(exe))
	out, err := exec.Command(ps, "-NoProfile", "-NonInteractive", "-Command", script).CombinedOutput()
	if err != nil {
		return map[string]any{"error": err.Error(), "output": string(out)}
	}
	var v any
	if json.Unmarshal(out, &v) != nil {
		return string(out)
	}
	return v
}
func psQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
func sanitizeFilename(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

type limitedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if b.limit <= 0 {
		return n, nil
	}
	remain := b.limit - b.buf.Len()
	if remain <= 0 {
		b.truncated = true
		return n, nil
	}
	if len(p) > remain {
		_, _ = b.buf.Write(p[:remain])
		b.truncated = true
		return n, nil
	}
	_, _ = b.buf.Write(p)
	return n, nil
}
func (b *limitedBuffer) String() string { return b.buf.String() }

// Quietly keep bufio imported in Windows builds where future stream mode may use it.
var _ = bufio.ErrInvalidUnreadByte
