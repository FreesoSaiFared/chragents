package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const artifactPushSchema = "minefield.artifact-push/1"

var uploadIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)
var sha256Pattern = regexp.MustCompile(`^[a-fA-F0-9]{64}$`)

type ArtifactPushManifest struct {
	Schema          string `json:"schema"`
	ID              string `json:"id"`
	State           string `json:"state"`
	FileName        string `json:"fileName"`
	ExpectedSHA256  string `json:"expectedSha256"`
	TotalBytes      int64  `json:"totalBytes"`
	ChunkCount      int    `json:"chunkCount"`
	OriginURL       string `json:"originUrl"`
	ConversationKey string `json:"conversationKey,omitempty"`
	TabActorID      string `json:"tabActorId,omitempty"`
	TabID           int    `json:"tabId,omitempty"`
	AutoIngest      bool   `json:"autoIngest"`
	CreatedAt       string `json:"createdAt"`
	UpdatedAt       string `json:"updatedAt"`
}

func (cp *ControlPlane) artifactPushRoot() string {
	return filepath.Join(cp.root, "uploads")
}

func (cp *ControlPlane) artifactPushSessionDir(id string) string {
	return filepath.Join(cp.artifactPushRoot(), "inflight", id)
}

func normalizeUploadID(value string) (string, error) {
	value = strings.TrimSpace(value)
	if !uploadIDPattern.MatchString(value) {
		return "", errors.New("UPLOAD_ID_INVALID")
	}
	return value, nil
}

func normalizeUploadFileName(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || value != filepath.Base(value) || strings.ContainsAny(value, `/\\`) {
		return "", errors.New("UPLOAD_FILENAME_INVALID")
	}
	if !strings.EqualFold(filepath.Ext(value), ".zip") {
		return "", errors.New("UPLOAD_ONLY_ZIP_SUPPORTED")
	}
	if numberedDuplicatePattern.MatchString(value) {
		return "", errors.New("OUT_OF_SYNC_DUPLICATE_FILENAME")
	}
	return value, nil
}

func payloadInt64(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	case float64:
		return int64(x)
	case json.Number:
		n, _ := x.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		return n
	default:
		return 0
	}
}

func (cp *ControlPlane) executeArtifactPush(env commandEnvelope, policy ArtifactPolicy, action string) map[string]any {
	switch action {
	case "push.begin", "upload.begin":
		return cp.beginArtifactPush(env, policy)
	case "push.chunk", "upload.chunk":
		return cp.writeArtifactPushChunk(env, policy)
	case "push.commit", "upload.commit":
		return cp.commitArtifactPush(env, policy)
	case "push.status", "upload.status":
		return cp.statusArtifactPush(env)
	case "push.abort", "upload.abort":
		return cp.abortArtifactPush(env)
	default:
		return map[string]any{"ok": false, "error": "ARTIFACT_PUSH_ACTION_NOT_SUPPORTED", "action": action}
	}
}

func (cp *ControlPlane) beginArtifactPush(env commandEnvelope, policy ArtifactPolicy) map[string]any {
	id, err := normalizeUploadID(stringAny(env.Payload["uploadId"]))
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	fileName, err := normalizeUploadFileName(stringAny(env.Payload["fileName"]))
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	expected := strings.ToLower(strings.TrimSpace(stringAny(env.Payload["sha256"])))
	if !sha256Pattern.MatchString(expected) {
		return map[string]any{"ok": false, "error": "UPLOAD_SHA256_INVALID"}
	}
	totalBytes := payloadInt64(env.Payload["totalBytes"])
	chunkCount := intAny(env.Payload["chunkCount"])
	if totalBytes <= 0 || (policy.MaxArchiveBytes > 0 && totalBytes > policy.MaxArchiveBytes) {
		return map[string]any{"ok": false, "error": "UPLOAD_SIZE_INVALID", "totalBytes": totalBytes}
	}
	if chunkCount <= 0 || chunkCount > 65536 {
		return map[string]any{"ok": false, "error": "UPLOAD_CHUNK_COUNT_INVALID", "chunkCount": chunkCount}
	}
	manifest := ArtifactPushManifest{
		Schema: artifactPushSchema, ID: id, State: "RECEIVING", FileName: fileName,
		ExpectedSHA256: expected, TotalBytes: totalBytes, ChunkCount: chunkCount,
		OriginURL: env.OriginURL, ConversationKey: strings.TrimSpace(stringAny(env.Payload["conversationKey"])),
		TabActorID: strings.TrimSpace(stringAny(env.Payload["tabActorId"])), TabID: env.TabID,
		AutoIngest: true, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if value, ok := env.Payload["autoIngest"]; ok {
		manifest.AutoIngest = boolAny(value)
	}
	dir := cp.artifactPushSessionDir(id)
	if existing, loadErr := cp.loadArtifactPush(id); loadErr == nil {
		if existing.FileName == manifest.FileName && existing.ExpectedSHA256 == manifest.ExpectedSHA256 && existing.TotalBytes == manifest.TotalBytes && existing.ChunkCount == manifest.ChunkCount && existing.OriginURL == manifest.OriginURL {
			return map[string]any{"ok": true, "outcome": "UPLOAD_ALREADY_BEGUN", "manifest": existing}
		}
		return map[string]any{"ok": false, "error": "UPLOAD_ID_CONFLICT", "existing": existing}
	}
	if err := os.MkdirAll(filepath.Join(dir, "chunks"), 0o700); err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	if err := writeJSONAtomic(filepath.Join(dir, "manifest.json"), manifest); err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	return map[string]any{"ok": true, "outcome": "UPLOAD_READY", "manifest": manifest}
}

func (cp *ControlPlane) loadArtifactPush(id string) (ArtifactPushManifest, error) {
	id, err := normalizeUploadID(id)
	if err != nil {
		return ArtifactPushManifest{}, err
	}
	data, err := os.ReadFile(filepath.Join(cp.artifactPushSessionDir(id), "manifest.json"))
	if err != nil {
		return ArtifactPushManifest{}, err
	}
	var manifest ArtifactPushManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return ArtifactPushManifest{}, err
	}
	if manifest.Schema != artifactPushSchema || manifest.ID != id {
		return ArtifactPushManifest{}, errors.New("UPLOAD_MANIFEST_INVALID")
	}
	return manifest, nil
}

func (cp *ControlPlane) requireUploadOrigin(manifest ArtifactPushManifest, env commandEnvelope) error {
	if manifest.OriginURL == "" || env.OriginURL == "" || manifest.OriginURL != env.OriginURL {
		return errors.New("UPLOAD_ORIGIN_MISMATCH")
	}
	return nil
}

func (cp *ControlPlane) writeArtifactPushChunk(env commandEnvelope, policy ArtifactPolicy) map[string]any {
	id := strings.TrimSpace(stringAny(env.Payload["uploadId"]))
	manifest, err := cp.loadArtifactPush(id)
	if err != nil {
		return map[string]any{"ok": false, "error": "UPLOAD_NOT_FOUND", "detail": err.Error()}
	}
	if err := cp.requireUploadOrigin(manifest, env); err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	if manifest.State != "RECEIVING" {
		return map[string]any{"ok": false, "error": "UPLOAD_NOT_RECEIVING", "state": manifest.State}
	}
	index := intAny(env.Payload["index"])
	if index < 0 || index >= manifest.ChunkCount {
		return map[string]any{"ok": false, "error": "UPLOAD_CHUNK_INDEX_INVALID", "index": index}
	}
	encoded := strings.TrimSpace(stringAny(env.Payload["dataBase64"]))
	if encoded == "" || len(encoded) > 3_000_000 {
		return map[string]any{"ok": false, "error": "UPLOAD_CHUNK_DATA_INVALID"}
	}
	data, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return map[string]any{"ok": false, "error": "UPLOAD_CHUNK_BASE64_INVALID", "detail": err.Error()}
	}
	if len(data) == 0 || len(data) > 2<<20 {
		return map[string]any{"ok": false, "error": "UPLOAD_CHUNK_SIZE_INVALID", "bytes": len(data)}
	}
	expected := strings.ToLower(strings.TrimSpace(stringAny(env.Payload["chunkSha256"])))
	if !sha256Pattern.MatchString(expected) {
		return map[string]any{"ok": false, "error": "UPLOAD_CHUNK_SHA256_REQUIRED"}
	}
	digest := sha256.Sum256(data)
	actual := hex.EncodeToString(digest[:])
	if actual != expected {
		return map[string]any{"ok": false, "error": "UPLOAD_CHUNK_SHA256_MISMATCH", "expected": expected, "actual": actual}
	}
	path := filepath.Join(cp.artifactPushSessionDir(id), "chunks", fmt.Sprintf("%06d.part", index))
	if existing, readErr := os.ReadFile(path); readErr == nil {
		old := sha256.Sum256(existing)
		oldHash := hex.EncodeToString(old[:])
		if oldHash == actual {
			return map[string]any{"ok": true, "outcome": "UPLOAD_CHUNK_DUPLICATE_SUPPRESSED", "index": index, "sha256": actual}
		}
		return map[string]any{"ok": false, "error": "UPLOAD_CHUNK_CONFLICT", "index": index, "existingSha256": oldHash, "incomingSha256": actual}
	}
	if err := writeAtomic(path, data); err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	manifest.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	_ = writeJSONAtomic(filepath.Join(cp.artifactPushSessionDir(id), "manifest.json"), manifest)
	return map[string]any{"ok": true, "outcome": "UPLOAD_CHUNK_ACCEPTED", "index": index, "bytes": len(data), "sha256": actual}
}

func (cp *ControlPlane) statusArtifactPush(env commandEnvelope) map[string]any {
	id := strings.TrimSpace(stringAny(env.Payload["uploadId"]))
	manifest, err := cp.loadArtifactPush(id)
	if err != nil {
		return map[string]any{"ok": false, "error": "UPLOAD_NOT_FOUND", "detail": err.Error()}
	}
	if err := cp.requireUploadOrigin(manifest, env); err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	entries, _ := os.ReadDir(filepath.Join(cp.artifactPushSessionDir(id), "chunks"))
	indexes := make([]int, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".part") {
			continue
		}
		n, parseErr := strconv.Atoi(strings.TrimSuffix(entry.Name(), ".part"))
		if parseErr == nil {
			indexes = append(indexes, n)
		}
	}
	sort.Ints(indexes)
	return map[string]any{"ok": true, "manifest": manifest, "receivedIndexes": indexes, "receivedCount": len(indexes), "missingCount": manifest.ChunkCount - len(indexes)}
}

func (cp *ControlPlane) commitArtifactPush(env commandEnvelope, policy ArtifactPolicy) map[string]any {
	id := strings.TrimSpace(stringAny(env.Payload["uploadId"]))
	manifest, err := cp.loadArtifactPush(id)
	if err != nil {
		return map[string]any{"ok": false, "error": "UPLOAD_NOT_FOUND", "detail": err.Error()}
	}
	if err := cp.requireUploadOrigin(manifest, env); err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	target := filepath.Join(policy.DownloadsRoot, manifest.FileName)
	if !pathAllowed(target, cp.config.Filesystem.AllowedRoots) {
		return map[string]any{"ok": false, "error": "UPLOAD_TARGET_DENIED", "target": target}
	}
	if _, statErr := os.Stat(target); statErr == nil {
		return map[string]any{"ok": false, "error": "OUT_OF_SYNC_TARGET_FILE_EXISTS", "target": target}
	}
	if err := os.MkdirAll(policy.DownloadsRoot, 0o755); err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	tmp, err := os.CreateTemp(policy.DownloadsRoot, ".mf-upload-*.tmp")
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		_ = tmp.Close()
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()
	h := sha256.New()
	var total int64
	for index := 0; index < manifest.ChunkCount; index++ {
		path := filepath.Join(cp.artifactPushSessionDir(id), "chunks", fmt.Sprintf("%06d.part", index))
		part, openErr := os.Open(path)
		if openErr != nil {
			return map[string]any{"ok": false, "error": "UPLOAD_CHUNK_MISSING", "index": index}
		}
		n, copyErr := io.Copy(io.MultiWriter(tmp, h), part)
		_ = part.Close()
		if copyErr != nil {
			return map[string]any{"ok": false, "error": copyErr.Error(), "index": index}
		}
		total += n
		if policy.MaxArchiveBytes > 0 && total > policy.MaxArchiveBytes {
			return map[string]any{"ok": false, "error": "UPLOAD_TOO_LARGE_DURING_COMMIT"}
		}
	}
	if err := tmp.Sync(); err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	if err := tmp.Close(); err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	actual := hex.EncodeToString(h.Sum(nil))
	if total != manifest.TotalBytes {
		return map[string]any{"ok": false, "error": "UPLOAD_TOTAL_SIZE_MISMATCH", "expected": manifest.TotalBytes, "actual": total}
	}
	if actual != manifest.ExpectedSHA256 {
		return map[string]any{"ok": false, "error": "UPLOAD_SHA256_MISMATCH", "expected": manifest.ExpectedSHA256, "actual": actual}
	}
	origin := ArtifactOrigin{Schema: artifactSchema, ExpectedName: manifest.FileName, FinalPath: target, OriginURL: manifest.OriginURL, ConversationKey: manifest.ConversationKey, TabActorID: manifest.TabActorID, TabID: manifest.TabID, RegisteredAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if _, err := cp.saveArtifactOrigin(origin); err != nil {
		return map[string]any{"ok": false, "error": "UPLOAD_ORIGIN_PERSIST_FAILED", "detail": err.Error()}
	}
	if err := os.Rename(tmpPath, target); err != nil {
		return map[string]any{"ok": false, "error": "UPLOAD_COMMIT_RENAME_FAILED", "detail": err.Error()}
	}
	committed = true
	manifest.State = "COMMITTED"
	manifest.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	completedRoot := filepath.Join(cp.artifactPushRoot(), "completed")
	_ = os.MkdirAll(completedRoot, 0o700)
	_ = writeJSONAtomic(filepath.Join(completedRoot, id+".json"), manifest)
	_ = os.RemoveAll(cp.artifactPushSessionDir(id))
	result := map[string]any{"ok": true, "outcome": "UPLOAD_COMMITTED", "path": target, "bytes": total, "sha256": actual, "autoIngest": manifest.AutoIngest}
	if manifest.AutoIngest {
		receipt := cp.processZipArtifact(target, manifest.OriginURL, policy)
		result["receipt"] = receipt
		result["ok"] = receipt.Status == "EXTRACTED"
		if receipt.Error != "" {
			result["error"] = receipt.Error
		}
	}
	return result
}

func (cp *ControlPlane) abortArtifactPush(env commandEnvelope) map[string]any {
	id := strings.TrimSpace(stringAny(env.Payload["uploadId"]))
	manifest, err := cp.loadArtifactPush(id)
	if err != nil {
		return map[string]any{"ok": false, "error": "UPLOAD_NOT_FOUND", "detail": err.Error()}
	}
	if err := cp.requireUploadOrigin(manifest, env); err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	if err := os.RemoveAll(cp.artifactPushSessionDir(id)); err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	return map[string]any{"ok": true, "outcome": "UPLOAD_ABORTED", "uploadId": id}
}
