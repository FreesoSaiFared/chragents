package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

const locatorSchema = "minefield.chat-location/1"

type ChatLocation struct {
	Schema            string `json:"schema"`
	ChatKey           string `json:"chatKey"`
	CanonicalURL      string `json:"canonicalUrl"`
	Provider          string `json:"provider"`
	ConversationID    string `json:"conversationId,omitempty"`
	ProjectID         string `json:"projectId,omitempty"`
	MachineID         string `json:"machineId"`
	BrowserInstanceID string `json:"browserInstanceId"`
	ProfileID         string `json:"profileId"`
	TabActorID        string `json:"tabActorId"`
	TabID             int    `json:"tabId,omitempty"`
	WindowID          int    `json:"windowId,omitempty"`
	Generation        string `json:"generation,omitempty"`
	State             string `json:"state"`
	AdvertiseURL      string `json:"advertiseUrl,omitempty"`
	RegisteredAt      string `json:"registeredAt"`
	ExpiresAt         string `json:"expiresAt"`
}

type LocatorResolution struct {
	Schema    string         `json:"schema"`
	OK        bool           `json:"ok"`
	ChatKey   string         `json:"chatKey"`
	Locations []ChatLocation `json:"locations"`
	Source    string         `json:"source"`
	Error     string         `json:"error,omitempty"`
}

func stableMachineID() string {
	host, _ := os.Hostname()
	seed := strings.ToLower(strings.TrimSpace(host)) + "|" + runtime.GOOS + "|" + runtime.GOARCH
	h := sha256.Sum256([]byte(seed))
	return "mf-machine-" + hex.EncodeToString(h[:8])
}

func canonicalChatIdentity(raw string) (key, canonical, provider, projectID, conversationID string, err error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", "", "", "", "", errors.New("INVALID_CHAT_URL")
	}
	host := strings.ToLower(u.Hostname())
	parts := strings.FieldsFunc(u.EscapedPath(), func(r rune) bool { return r == '/' })
	if host == "chatgpt.com" || host == "chat.openai.com" {
		provider = "chatgpt"
		for i, p := range parts {
			decoded, _ := url.PathUnescape(p)
			if strings.HasPrefix(decoded, "g-p-") {
				projectID = decoded
			}
			if decoded == "c" && i+1 < len(parts) {
				conversationID, _ = url.PathUnescape(parts[i+1])
				break
			}
		}
		if conversationID == "" {
			return "", "", provider, projectID, "", errors.New("CHATGPT_CONVERSATION_ID_NOT_FOUND")
		}
		canonical = "https://chatgpt.com/"
		if projectID != "" {
			canonical += "g/" + projectID + "/"
		}
		canonical += "c/" + conversationID
		key = "chat://chatgpt/"
		if projectID != "" {
			key += projectID + "/"
		}
		key += conversationID
		return key, canonical, provider, projectID, conversationID, nil
	}
	provider = host
	u.Fragment = ""
	u.RawQuery = ""
	u.Host = host
	canonical = strings.TrimRight(u.String(), "/")
	h := sha256.Sum256([]byte(canonical))
	key = "url://" + provider + "/" + hex.EncodeToString(h[:16])
	return key, canonical, provider, "", "", nil
}

func (cp *ControlPlane) locatorRoot() string {
	return filepath.Join(cp.root, "locator")
}

func (cp *ControlPlane) ensureLocatorRoot() error {
	return os.MkdirAll(filepath.Join(cp.locatorRoot(), "registry"), 0o700)
}

func (cp *ControlPlane) saveChatLocation(loc ChatLocation) (string, error) {
	if err := cp.ensureLocatorRoot(); err != nil {
		return "", err
	}
	if loc.Schema == "" {
		loc.Schema = locatorSchema
	}
	if loc.ChatKey == "" || loc.CanonicalURL == "" {
		key, canonical, provider, project, conversation, err := canonicalChatIdentity(loc.CanonicalURL)
		if err != nil {
			return "", err
		}
		loc.ChatKey, loc.CanonicalURL, loc.Provider, loc.ProjectID, loc.ConversationID = key, canonical, provider, project, conversation
	}
	cp.mu.RLock()
	cfg := cp.config.Locator
	cp.mu.RUnlock()
	if loc.MachineID == "" {
		loc.MachineID = cfg.MachineID
	}
	if loc.BrowserInstanceID == "" {
		loc.BrowserInstanceID = cfg.BrowserInstanceID
	}
	if loc.ProfileID == "" {
		loc.ProfileID = cfg.ProfileID
	}
	if loc.State == "" {
		loc.State = "BOUND"
	}
	now := time.Now().UTC()
	if loc.RegisteredAt == "" {
		loc.RegisteredAt = now.Format(time.RFC3339Nano)
	}
	if loc.ExpiresAt == "" {
		lease := cfg.LeaseSeconds
		if lease <= 0 {
			lease = 120
		}
		loc.ExpiresAt = now.Add(time.Duration(lease) * time.Second).Format(time.RFC3339Nano)
	}
	if loc.AdvertiseURL == "" {
		loc.AdvertiseURL = cfg.AdvertiseURL
	}
	name := sanitizeFilename(loc.ChatKey+"__"+loc.MachineID+"__"+loc.BrowserInstanceID+"__"+loc.ProfileID) + ".json"
	path := filepath.Join(cp.locatorRoot(), "registry", name)
	if err := writeJSONAtomic(path, loc); err != nil {
		return "", err
	}
	cp.recordEvent("locator.registered", map[string]any{"location": loc})
	return path, nil
}

func (cp *ControlPlane) localLocations(chatKey string) []ChatLocation {
	entries, _ := os.ReadDir(filepath.Join(cp.locatorRoot(), "registry"))
	var out []ChatLocation
	now := time.Now().UTC()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(cp.locatorRoot(), "registry", entry.Name()))
		if err != nil {
			continue
		}
		var loc ChatLocation
		if json.Unmarshal(data, &loc) != nil || loc.ChatKey != chatKey {
			continue
		}
		expires, err := time.Parse(time.RFC3339Nano, loc.ExpiresAt)
		if err == nil && now.After(expires) {
			continue
		}
		out = append(out, loc)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].MachineID == out[j].MachineID {
			return out[i].RegisteredAt > out[j].RegisteredAt
		}
		return out[i].MachineID < out[j].MachineID
	})
	return out
}

func (cp *ControlPlane) executeLocator(env commandEnvelope, cfg ControlConfig) map[string]any {
	if !cfg.Locator.Enabled {
		return map[string]any{"ok": false, "error": "LOCATOR_DISABLED"}
	}
	action := strings.ToLower(strings.TrimSpace(stringAny(env.Payload["action"])))
	switch action {
	case "register", "bind", "heartbeat":
		rawURL := strings.TrimSpace(stringAny(env.Payload["url"]))
		if rawURL == "" {
			rawURL = env.OriginURL
		}
		key, canonical, provider, project, conversation, err := canonicalChatIdentity(rawURL)
		if err != nil {
			return map[string]any{"ok": false, "error": err.Error()}
		}
		loc := ChatLocation{
			Schema: locatorSchema, ChatKey: key, CanonicalURL: canonical, Provider: provider,
			ProjectID: project, ConversationID: conversation,
			MachineID: cfg.Locator.MachineID, BrowserInstanceID: cfg.Locator.BrowserInstanceID,
			ProfileID: cfg.Locator.ProfileID, TabActorID: strings.TrimSpace(stringAny(env.Payload["tabActorId"])),
			TabID: intAny(env.Payload["tabId"]), WindowID: intAny(env.Payload["windowId"]),
			Generation: strings.TrimSpace(stringAny(env.Payload["generation"])),
			State:      strings.TrimSpace(stringAny(env.Payload["state"])), AdvertiseURL: cfg.Locator.AdvertiseURL,
		}
		path, err := cp.saveChatLocation(loc)
		if err != nil {
			return map[string]any{"ok": false, "error": err.Error()}
		}
		return map[string]any{"ok": true, "chatKey": key, "canonicalUrl": canonical, "path": path, "location": loc}
	case "resolve":
		key := strings.TrimSpace(stringAny(env.Payload["chatKey"]))
		if key == "" {
			rawURL := strings.TrimSpace(stringAny(env.Payload["url"]))
			resolved, _, _, _, _, err := canonicalChatIdentity(rawURL)
			if err != nil {
				return map[string]any{"ok": false, "error": err.Error()}
			}
			key = resolved
		}
		resolution := cp.resolveEverywhere(key, cfg.Locator)
		return map[string]any{"ok": resolution.OK, "resolution": resolution, "error": resolution.Error}
	case "list":
		return map[string]any{"ok": true, "registryRoot": filepath.Join(cp.locatorRoot(), "registry"), "machineId": cfg.Locator.MachineID, "peers": cfg.Locator.PeerEndpoints}
	default:
		return map[string]any{"ok": false, "error": "LOCATOR_ACTION_NOT_SUPPORTED", "action": action}
	}
}

func (cp *ControlPlane) resolveEverywhere(chatKey string, policy LocatorPolicy) LocatorResolution {
	local := cp.localLocations(chatKey)
	resolution := LocatorResolution{Schema: "minefield.chat-location-resolution/1", ChatKey: chatKey, Locations: local, Source: "local"}
	seen := map[string]bool{}
	for _, loc := range local {
		seen[loc.MachineID+"|"+loc.BrowserInstanceID+"|"+loc.ProfileID+"|"+loc.TabActorID] = true
	}
	for _, peer := range policy.PeerEndpoints {
		peer = strings.TrimRight(strings.TrimSpace(peer), "/")
		if peer == "" {
			continue
		}
		remote, err := queryLocatorPeer(peer, chatKey, policy.PeerSharedSecret)
		if err != nil {
			continue
		}
		for _, loc := range remote.Locations {
			identity := loc.MachineID + "|" + loc.BrowserInstanceID + "|" + loc.ProfileID + "|" + loc.TabActorID
			if !seen[identity] {
				resolution.Locations = append(resolution.Locations, loc)
				seen[identity] = true
			}
		}
	}
	resolution.OK = len(resolution.Locations) > 0
	if !resolution.OK {
		resolution.Error = "CHAT_ACTOR_NOT_FOUND"
	}
	if len(resolution.Locations) > len(local) {
		resolution.Source = "local+peers"
	}
	return resolution
}

func queryLocatorPeer(endpoint, chatKey, secret string) (LocatorResolution, error) {
	path := "/peer/resolve?chatKey=" + url.QueryEscape(chatKey)
	req, err := http.NewRequest(http.MethodGet, endpoint+path, nil)
	if err != nil {
		return LocatorResolution{}, err
	}
	signPeerRequest(req, nil, secret)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return LocatorResolution{}, err
	}
	defer resp.Body.Close()
	var out LocatorResolution
	if json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&out) != nil || resp.StatusCode >= 300 {
		return LocatorResolution{}, errors.New("PEER_RESOLVE_FAILED")
	}
	return out, nil
}

func signPeerRequest(req *http.Request, body []byte, secret string) {
	ts := fmt.Sprintf("%d", time.Now().Unix())
	bodyHash := sha256.Sum256(body)
	canonical := ts + "\n" + req.Method + "\n" + req.URL.RequestURI() + "\n" + hex.EncodeToString(bodyHash[:])
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(canonical))
	req.Header.Set("X-MF-Timestamp", ts)
	req.Header.Set("X-MF-Signature", hex.EncodeToString(mac.Sum(nil)))
}

func verifyPeerRequest(req *http.Request, body []byte, secret string) error {
	ts := req.Header.Get("X-MF-Timestamp")
	sig := req.Header.Get("X-MF-Signature")
	if ts == "" || sig == "" || secret == "" {
		return errors.New("PEER_AUTH_REQUIRED")
	}
	unix, err := parseInt64(ts)
	if err != nil || time.Since(time.Unix(unix, 0)) > 2*time.Minute || time.Until(time.Unix(unix, 0)) > 2*time.Minute {
		return errors.New("PEER_TIMESTAMP_INVALID")
	}
	bodyHash := sha256.Sum256(body)
	canonical := ts + "\n" + req.Method + "\n" + req.URL.RequestURI() + "\n" + hex.EncodeToString(bodyHash[:])
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(canonical))
	expected, err := hex.DecodeString(sig)
	if err != nil || !hmac.Equal(expected, mac.Sum(nil)) {
		return errors.New("PEER_SIGNATURE_INVALID")
	}
	return nil
}

func parseInt64(text string) (int64, error) {
	var n int64
	_, err := fmt.Sscanf(text, "%d", &n)
	return n, err
}

func (cp *ControlPlane) startLocatorPeerServer() {
	cp.mu.RLock()
	policy := cp.config.Locator
	cp.mu.RUnlock()
	if !policy.Enabled || strings.TrimSpace(policy.ListenAddress) == "" {
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/peer/resolve", func(w http.ResponseWriter, r *http.Request) {
		if !policy.AllowRemoteResolve {
			writeHTTPJSON(w, 403, LocatorResolution{Schema: "minefield.chat-location-resolution/1", Error: "REMOTE_RESOLVE_DISABLED"})
			return
		}
		if err := verifyPeerRequest(r, nil, policy.PeerSharedSecret); err != nil {
			writeHTTPJSON(w, 401, LocatorResolution{Schema: "minefield.chat-location-resolution/1", Error: err.Error()})
			return
		}
		key := r.URL.Query().Get("chatKey")
		locations := cp.localLocations(key)
		writeHTTPJSON(w, 200, LocatorResolution{Schema: "minefield.chat-location-resolution/1", OK: len(locations) > 0, ChatKey: key, Locations: locations, Source: "peer-local"})
	})
	mux.HandleFunc("/peer/register", func(w http.ResponseWriter, r *http.Request) {
		if !policy.AllowRemoteRegister {
			writeHTTPJSON(w, 403, map[string]any{"ok": false, "error": "REMOTE_REGISTER_DISABLED"})
			return
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 2<<20))
		if err := verifyPeerRequest(r, body, policy.PeerSharedSecret); err != nil {
			writeHTTPJSON(w, 401, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		var loc ChatLocation
		if json.Unmarshal(body, &loc) != nil {
			writeHTTPJSON(w, 400, map[string]any{"ok": false, "error": "INVALID_LOCATION"})
			return
		}
		path, err := cp.saveChatLocation(loc)
		if err != nil {
			writeHTTPJSON(w, 500, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeHTTPJSON(w, 200, map[string]any{"ok": true, "path": path})
	})
	listener, err := net.Listen("tcp", policy.ListenAddress)
	if err != nil {
		cp.recordEvent("locator.peer-listen-failed", map[string]any{"address": policy.ListenAddress, "error": err.Error()})
		return
	}
	cp.recordEvent("locator.peer-listening", map[string]any{"address": policy.ListenAddress, "advertiseUrl": policy.AdvertiseURL})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		cp.recordEvent("locator.peer-server-failed", map[string]any{"error": err.Error()})
	}
}

func (cp *ControlPlane) handleLocatorRegister(w http.ResponseWriter, r *http.Request) {
	if err := cp.authorize(r); err != nil {
		writeHTTPJSON(w, 401, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	var loc ChatLocation
	if json.Unmarshal(body, &loc) != nil {
		writeHTTPJSON(w, 400, map[string]any{"ok": false, "error": "INVALID_LOCATION"})
		return
	}
	path, err := cp.saveChatLocation(loc)
	if err != nil {
		writeHTTPJSON(w, 500, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeHTTPJSON(w, 200, map[string]any{"ok": true, "path": path, "location": loc})
}

func (cp *ControlPlane) handleLocatorResolve(w http.ResponseWriter, r *http.Request) {
	if err := cp.authorize(r); err != nil {
		writeHTTPJSON(w, 401, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	key := r.URL.Query().Get("chatKey")
	if key == "" {
		rawURL := r.URL.Query().Get("url")
		resolved, _, _, _, _, err := canonicalChatIdentity(rawURL)
		if err != nil {
			writeHTTPJSON(w, 400, LocatorResolution{Schema: "minefield.chat-location-resolution/1", Error: err.Error()})
			return
		}
		key = resolved
	}
	cp.mu.RLock()
	policy := cp.config.Locator
	cp.mu.RUnlock()
	resolution := cp.resolveEverywhere(key, policy)
	status := 200
	if !resolution.OK {
		status = 404
	}
	writeHTTPJSON(w, status, resolution)
}

func peerRequestBody(loc ChatLocation) ([]byte, error) {
	return json.Marshal(loc)
}

func sendPeerRegister(endpoint string, loc ChatLocation, secret string) error {
	body, err := peerRequestBody(loc)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(endpoint, "/")+"/peer/register", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	signPeerRequest(req, body, secret)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("peer register status %d", resp.StatusCode)
	}
	return nil
}
