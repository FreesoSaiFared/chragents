package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const artifactSchema = "minefield.artifact-intake/1"

var numberedDuplicatePattern = regexp.MustCompile(`(?i)^(.*)\(([1-9][0-9]*)\)(\.[^.]+)$`)

type ArtifactOrigin struct {
	Schema          string `json:"schema"`
	DownloadID      int    `json:"downloadId,omitempty"`
	ExpectedName    string `json:"expectedName,omitempty"`
	FinalPath       string `json:"finalPath,omitempty"`
	SourceURL       string `json:"sourceUrl,omitempty"`
	OriginURL       string `json:"originUrl"`
	ConversationKey string `json:"conversationKey,omitempty"`
	TabActorID      string `json:"tabActorId,omitempty"`
	TabID           int    `json:"tabId,omitempty"`
	RegisteredAt    string `json:"registeredAt"`
}

type ArtifactReceipt struct {
	Schema          string         `json:"schema"`
	ID              string         `json:"id"`
	Status          string         `json:"status"`
	ArchivePath     string         `json:"archivePath"`
	ArchiveName     string         `json:"archiveName"`
	ArchiveSHA256   string         `json:"archiveSha256,omitempty"`
	TargetDirectory string         `json:"targetDirectory,omitempty"`
	EntryCount      int            `json:"entryCount,omitempty"`
	ExpandedBytes   uint64         `json:"expandedBytes,omitempty"`
	CompressedBytes uint64         `json:"compressedBytes,omitempty"`
	Origin          ArtifactOrigin `json:"origin,omitempty"`
	StartedAt       string         `json:"startedAt"`
	CompletedAt     string         `json:"completedAt"`
	Error           string         `json:"error,omitempty"`
	Evidence        map[string]any `json:"evidence,omitempty"`
}

type ReturnEnvelope struct {
	State           string         `json:"state"`
	Schema          string         `json:"schema"`
	ID              string         `json:"id"`
	Kind            string         `json:"kind"`
	OriginURL       string         `json:"originUrl"`
	ConversationKey string         `json:"conversationKey,omitempty"`
	TabActorID      string         `json:"tabActorId,omitempty"`
	TabID           int            `json:"tabId,omitempty"`
	Payload         map[string]any `json:"payload"`
	CreatedAt       string         `json:"createdAt"`
	Attempts        int            `json:"attempts"`
	LastAttemptAt   string         `json:"lastAttemptAt,omitempty"`
}

func (cp *ControlPlane) artifactRoot() string {
	return filepath.Join(cp.root, "artifacts")
}

func (cp *ControlPlane) originRoot() string {
	return filepath.Join(cp.artifactRoot(), "origins")
}

func (cp *ControlPlane) returnPendingRoot() string {
	return filepath.Join(cp.root, "returns", "pending")
}

func (cp *ControlPlane) returnAckRoot() string {
	return filepath.Join(cp.root, "returns", "acked")
}

func (cp *ControlPlane) ensureArtifactRoots() error {
	for _, dir := range []string{cp.artifactRoot(), cp.originRoot(), cp.returnPendingRoot(), cp.returnAckRoot()} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func (cp *ControlPlane) executeArtifact(env commandEnvelope, cfg ControlConfig) map[string]any {
	if !cfg.Artifacts.Enabled {
		return map[string]any{"ok": false, "error": "ARTIFACT_INTAKE_DISABLED"}
	}
	action := strings.ToLower(strings.TrimSpace(stringAny(env.Payload["action"])))
	if action == "" {
		action = "ingest"
	}
	switch action {
	case "register":
		origin := ArtifactOrigin{
			Schema: artifactSchema, DownloadID: intAny(env.Payload["downloadId"]),
			ExpectedName:    strings.TrimSpace(stringAny(env.Payload["expectedName"])),
			FinalPath:       strings.TrimSpace(stringAny(env.Payload["finalPath"])),
			SourceURL:       strings.TrimSpace(stringAny(env.Payload["sourceUrl"])),
			OriginURL:       env.OriginURL,
			ConversationKey: strings.TrimSpace(stringAny(env.Payload["conversationKey"])),
			TabActorID:      strings.TrimSpace(stringAny(env.Payload["tabActorId"])),
			TabID:           env.TabID,
			RegisteredAt:    time.Now().UTC().Format(time.RFC3339Nano),
		}
		if origin.FinalPath != "" && !pathAllowed(origin.FinalPath, cfg.Filesystem.AllowedRoots) {
			return map[string]any{"ok": false, "error": "FINAL_PATH_DENIED", "path": origin.FinalPath}
		}
		path, err := cp.saveArtifactOrigin(origin)
		if err != nil {
			return map[string]any{"ok": false, "error": err.Error()}
		}
		return map[string]any{"ok": true, "registered": path, "origin": origin}
	case "ingest", "extract":
		path := strings.TrimSpace(stringAny(env.Payload["path"]))
		if path == "" {
			path = strings.TrimSpace(stringAny(env.Payload["detectedPath"]))
		}
		if path == "" {
			return map[string]any{"ok": false, "error": "ARCHIVE_PATH_REQUIRED"}
		}
		if !pathAllowed(path, cfg.Filesystem.AllowedRoots) {
			return map[string]any{"ok": false, "error": "ARCHIVE_PATH_DENIED", "path": path}
		}
		receipt := cp.processZipArtifact(path, env.OriginURL, cfg.Artifacts)
		return map[string]any{"ok": receipt.Status == "EXTRACTED", "receipt": receipt, "status": receipt.Status, "error": receipt.Error}
	case "push.begin", "upload.begin", "push.chunk", "upload.chunk", "push.commit", "upload.commit", "push.status", "upload.status", "push.abort", "upload.abort":
		return cp.executeArtifactPush(env, cfg.Artifacts, action)
	case "reconcile":
		results := cp.reconcileZipArtifacts(cfg.Artifacts)
		return map[string]any{"ok": true, "results": results, "count": len(results)}
	case "status":
		pending, _ := os.ReadDir(cp.returnPendingRoot())
		origins, _ := os.ReadDir(cp.originRoot())
		return map[string]any{"ok": true, "pendingReturns": len(pending), "registeredOrigins": len(origins), "root": cp.artifactRoot()}
	default:
		return map[string]any{"ok": false, "error": "ARTIFACT_ACTION_NOT_SUPPORTED", "action": action}
	}
}

func (cp *ControlPlane) saveArtifactOrigin(origin ArtifactOrigin) (string, error) {
	if err := cp.ensureArtifactRoots(); err != nil {
		return "", err
	}
	key := origin.ExpectedName
	if origin.FinalPath != "" {
		key = filepath.Base(origin.FinalPath)
	}
	if key == "" && origin.DownloadID > 0 {
		key = fmt.Sprintf("download-%d", origin.DownloadID)
	}
	if key == "" {
		key = randomToken(12)
	}
	name := sanitizeFilename(strings.ToLower(key)) + "-" + randomToken(6) + ".json"
	path := filepath.Join(cp.originRoot(), name)
	return path, writeJSONAtomic(path, origin)
}

func (cp *ControlPlane) findArtifactOrigin(path string) ArtifactOrigin {
	var candidates []struct {
		origin ArtifactOrigin
		mtime  time.Time
	}
	entries, _ := os.ReadDir(cp.originRoot())
	base := strings.ToLower(filepath.Base(path))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(cp.originRoot(), entry.Name()))
		if err != nil {
			continue
		}
		var origin ArtifactOrigin
		if json.Unmarshal(data, &origin) != nil {
			continue
		}
		match := origin.FinalPath != "" && samePath(origin.FinalPath, path)
		if !match && origin.ExpectedName != "" {
			match = strings.EqualFold(filepath.Base(origin.ExpectedName), base)
		}
		if !match {
			continue
		}
		info, _ := entry.Info()
		candidates = append(candidates, struct {
			origin ArtifactOrigin
			mtime  time.Time
		}{origin, info.ModTime()})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].mtime.After(candidates[j].mtime) })
	if len(candidates) > 0 {
		return candidates[0].origin
	}
	return ArtifactOrigin{Schema: artifactSchema, OriginURL: "", RegisteredAt: time.Now().UTC().Format(time.RFC3339Nano)}
}

func (cp *ControlPlane) processZipArtifact(path, fallbackOrigin string, policy ArtifactPolicy) ArtifactReceipt {
	started := time.Now().UTC()
	receipt := ArtifactReceipt{
		Schema: artifactSchema, ID: "artifact-" + randomToken(12), Status: "FAILED",
		ArchivePath: filepath.Clean(path), ArchiveName: filepath.Base(path),
		StartedAt: started.Format(time.RFC3339Nano), Evidence: map[string]any{},
	}
	defer func() {
		receipt.CompletedAt = time.Now().UTC().Format(time.RFC3339Nano)
		_ = cp.persistArtifactReceipt(receipt)
	}()
	origin := cp.findArtifactOrigin(path)
	if origin.OriginURL == "" {
		origin.OriginURL = fallbackOrigin
	}
	receipt.Origin = origin

	info, err := os.Stat(path)
	if err != nil {
		receipt.Status, receipt.Error = "ARCHIVE_NOT_FOUND", err.Error()
		cp.emitArtifactReturn(receipt)
		return receipt
	}
	if info.IsDir() {
		receipt.Status, receipt.Error = "ARCHIVE_PATH_IS_DIRECTORY", "archive path is a directory"
		cp.emitArtifactReturn(receipt)
		return receipt
	}
	if policy.MaxArchiveBytes > 0 && info.Size() > policy.MaxArchiveBytes {
		receipt.Status, receipt.Error = "ARCHIVE_TOO_LARGE", fmt.Sprintf("%d > %d", info.Size(), policy.MaxArchiveBytes)
		cp.emitArtifactReturn(receipt)
		return receipt
	}
	if ext := strings.ToLower(filepath.Ext(path)); ext != ".zip" {
		receipt.Status, receipt.Error = "NOT_A_ZIP", "file extension is not .zip"
		cp.emitArtifactReturn(receipt)
		return receipt
	}
	if match := numberedDuplicatePattern.FindStringSubmatch(filepath.Base(path)); policy.RejectNumberedDuplicates && match != nil {
		receipt.Status = "OUT_OF_SYNC_DUPLICATE_FILENAME"
		receipt.Error = fmt.Sprintf("numbered duplicate suffix (%s) indicates browser/download state divergence", match[2])
		receipt.Evidence["canonicalName"] = match[1] + match[3]
		receipt.Evidence["duplicateNumber"] = match[2]
		cp.emitArtifactReturn(receipt)
		return receipt
	}
	target := strings.TrimSuffix(path, filepath.Ext(path))
	receipt.TargetDirectory = target
	if _, err := os.Stat(target); err == nil && policy.RejectExistingDirectory {
		receipt.Status = "OUT_OF_SYNC_TARGET_EXISTS"
		receipt.Error = "target directory already exists; refusing merge or overwrite"
		cp.emitArtifactReturn(receipt)
		return receipt
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		receipt.Status, receipt.Error = "TARGET_STAT_FAILED", err.Error()
		cp.emitArtifactReturn(receipt)
		return receipt
	}

	hash, err := fileSHA256(path)
	if err != nil {
		receipt.Status, receipt.Error = "ARCHIVE_HASH_FAILED", err.Error()
		cp.emitArtifactReturn(receipt)
		return receipt
	}
	receipt.ArchiveSHA256 = hash

	zr, err := zip.OpenReader(path)
	if err != nil {
		receipt.Status, receipt.Error = "ZIP_OPEN_FAILED", err.Error()
		cp.emitArtifactReturn(receipt)
		return receipt
	}
	zipClosed := false
	defer func() {
		if !zipClosed {
			_ = zr.Close()
		}
	}()
	if policy.MaxEntries > 0 && len(zr.File) > policy.MaxEntries {
		receipt.Status, receipt.Error = "ZIP_ENTRY_LIMIT", fmt.Sprintf("%d > %d", len(zr.File), policy.MaxEntries)
		cp.emitArtifactReturn(receipt)
		return receipt
	}

	staging := filepath.Join(filepath.Dir(path), ".mf-extract-"+sanitizeFilename(filepath.Base(target))+"-"+randomToken(8)+".partial")
	if err := os.Mkdir(staging, 0o700); err != nil {
		receipt.Status, receipt.Error = "STAGING_CREATE_FAILED", err.Error()
		cp.emitArtifactReturn(receipt)
		return receipt
	}
	keepStaging := false
	defer func() {
		if !keepStaging {
			_ = os.RemoveAll(staging)
		}
	}()

	seen := map[string]struct{}{}
	var expanded, compressed uint64
	for _, entry := range zr.File {
		clean, validErr := validateZipEntry(entry, policy, seen)
		if validErr != nil {
			receipt.Status, receipt.Error = "ZIP_VALIDATION_FAILED", validErr.Error()
			cp.emitArtifactReturn(receipt)
			return receipt
		}
		seen[strings.ToLower(clean)] = struct{}{}
		expanded += entry.UncompressedSize64
		compressed += entry.CompressedSize64
		if policy.MaxExpandedBytes > 0 && expanded > uint64(policy.MaxExpandedBytes) {
			receipt.Status, receipt.Error = "ZIP_EXPANDED_SIZE_LIMIT", fmt.Sprintf("%d > %d", expanded, policy.MaxExpandedBytes)
			cp.emitArtifactReturn(receipt)
			return receipt
		}
	}
	receipt.EntryCount, receipt.ExpandedBytes, receipt.CompressedBytes = len(zr.File), expanded, compressed
	if compressed > 0 && policy.MaxCompressionRatio > 0 && float64(expanded)/float64(compressed) > policy.MaxCompressionRatio {
		receipt.Status, receipt.Error = "ZIP_TOTAL_COMPRESSION_RATIO_LIMIT", fmt.Sprintf("%.2f > %.2f", float64(expanded)/float64(compressed), policy.MaxCompressionRatio)
		cp.emitArtifactReturn(receipt)
		return receipt
	}

	for _, entry := range zr.File {
		clean, _ := validateZipEntry(entry, policy, map[string]struct{}{})
		destination := filepath.Join(staging, filepath.FromSlash(clean))
		if !pathInside(destination, staging) {
			receipt.Status, receipt.Error = "ZIP_PATH_ESCAPE", clean
			cp.emitArtifactReturn(receipt)
			return receipt
		}
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(destination, 0o755); err != nil {
				receipt.Status, receipt.Error = "ZIP_MKDIR_FAILED", err.Error()
				cp.emitArtifactReturn(receipt)
				return receipt
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			receipt.Status, receipt.Error = "ZIP_PARENT_CREATE_FAILED", err.Error()
			cp.emitArtifactReturn(receipt)
			return receipt
		}
		rc, err := entry.Open()
		if err != nil {
			receipt.Status, receipt.Error = "ZIP_ENTRY_OPEN_FAILED", err.Error()
			cp.emitArtifactReturn(receipt)
			return receipt
		}
		mode := fs.FileMode(0o644)
		if entry.Mode().Perm() != 0 {
			mode = entry.Mode().Perm() & 0o755
			if mode&0o222 == 0 {
				mode |= 0o200
			}
		}
		out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err == nil {
			_, err = io.Copy(out, io.LimitReader(rc, int64(entry.UncompressedSize64)+1))
			if syncErr := out.Sync(); err == nil {
				err = syncErr
			}
			closeErr := out.Close()
			if err == nil {
				err = closeErr
			}
		}
		_ = rc.Close()
		if err != nil {
			receipt.Status, receipt.Error = "ZIP_ENTRY_WRITE_FAILED", err.Error()
			cp.emitArtifactReturn(receipt)
			return receipt
		}
	}

	// Windows denies deleting or replacing an archive while zip.OpenReader still
	// owns its file handle. Close it explicitly before the atomic commit and ZIP
	// deletion; the deferred fallback covers every earlier return path.
	if err := zr.Close(); err != nil {
		receipt.Status, receipt.Error = "ZIP_CLOSE_FAILED", err.Error()
		cp.emitArtifactReturn(receipt)
		return receipt
	}
	zipClosed = true

	manifest := map[string]any{
		"schema": artifactSchema, "artifactId": receipt.ID, "archiveName": receipt.ArchiveName,
		"archiveSha256": receipt.ArchiveSHA256, "entryCount": receipt.EntryCount,
		"expandedBytes": receipt.ExpandedBytes, "sourceUrl": receipt.Origin.SourceURL,
		"originUrl": receipt.Origin.OriginURL, "extractedAt": time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := writeJSONAtomic(filepath.Join(staging, ".minefield-artifact-receipt.json"), manifest); err != nil {
		receipt.Status, receipt.Error = "RECEIPT_WRITE_FAILED", err.Error()
		cp.emitArtifactReturn(receipt)
		return receipt
	}
	if _, err := os.Stat(target); err == nil {
		receipt.Status, receipt.Error = "OUT_OF_SYNC_TARGET_APPEARED", "target directory appeared during extraction"
		cp.emitArtifactReturn(receipt)
		return receipt
	}
	if err := os.Rename(staging, target); err != nil {
		receipt.Status, receipt.Error = "ATOMIC_RENAME_FAILED", err.Error()
		cp.emitArtifactReturn(receipt)
		return receipt
	}
	keepStaging = true
	if _, err := os.Stat(filepath.Join(target, ".minefield-artifact-receipt.json")); err != nil {
		receipt.Status, receipt.Error = "POST_RENAME_VERIFY_FAILED", err.Error()
		cp.emitArtifactReturn(receipt)
		return receipt
	}

	receipt.Status = "EXTRACTED"
	receipt.CompletedAt = time.Now().UTC().Format(time.RFC3339Nano)
	// Persist a non-deliverable commit envelope before deleting the source ZIP.
	// A crash after this point is recovered by recoverCommittingReturns.
	if err := cp.persistArtifactReturn(receipt, "COMMITTING"); err != nil {
		receipt.Status, receipt.Error = "RETURN_PERSIST_FAILED", err.Error()
		return receipt
	}
	if policy.DeleteZipAfterExtract {
		if err := os.Remove(path); err != nil {
			receipt.Status, receipt.Error = "EXTRACTED_ZIP_DELETE_FAILED", err.Error()
			_ = cp.emitArtifactReturn(receipt)
			return receipt
		}
		receipt.Evidence["zipDeleted"] = true
	}
	if err := cp.emitArtifactReturn(receipt); err != nil {
		receipt.Status, receipt.Error = "RETURN_FINALIZE_FAILED", err.Error()
		return receipt
	}
	return receipt
}

func validateZipEntry(entry *zip.File, policy ArtifactPolicy, seen map[string]struct{}) (string, error) {
	name := strings.ReplaceAll(entry.Name, `\\`, `/`)
	if name == "" || strings.ContainsRune(name, '\x00') {
		return "", errors.New("empty or NUL-containing entry name")
	}
	if strings.HasPrefix(name, "/") || filepath.IsAbs(name) {
		return "", fmt.Errorf("absolute path denied: %q", name)
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(name)))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return "", fmt.Errorf("path traversal denied: %q", name)
	}
	if len(clean) > 1024 {
		return "", errors.New("entry path too long")
	}
	if _, exists := seen[strings.ToLower(clean)]; exists {
		return "", fmt.Errorf("duplicate normalized entry: %q", clean)
	}
	mode := entry.Mode()
	if mode&os.ModeSymlink != 0 || mode&os.ModeDevice != 0 || mode&os.ModeNamedPipe != 0 || mode&os.ModeSocket != 0 {
		return "", fmt.Errorf("special file denied: %q", clean)
	}
	if policy.MaxCompressionRatio > 0 && entry.CompressedSize64 > 0 && float64(entry.UncompressedSize64)/float64(entry.CompressedSize64) > policy.MaxCompressionRatio {
		return "", fmt.Errorf("entry compression ratio exceeds limit: %q", clean)
	}
	return clean, nil
}

func pathInside(path, root string) bool {
	absPath, err1 := filepath.Abs(path)
	absRoot, err2 := filepath.Abs(root)
	if err1 != nil || err2 != nil {
		return false
	}
	rel, err := filepath.Rel(absRoot, absPath)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func (cp *ControlPlane) persistArtifactReceipt(receipt ArtifactReceipt) error {
	if err := cp.ensureArtifactRoots(); err != nil {
		return err
	}
	path := filepath.Join(cp.artifactRoot(), "receipts")
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	if receipt.CompletedAt == "" {
		receipt.CompletedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if err := writeJSONAtomic(filepath.Join(path, receipt.ID+".json"), receipt); err != nil {
		return err
	}
	_ = writeJSONAtomic(filepath.Join(cp.artifactRoot(), "MINEFIELD-ARTIFACT__RESULT__LATEST.json"), receipt)
	cp.recordEvent("artifact."+strings.ToLower(receipt.Status), map[string]any{"receipt": receipt})
	return nil
}

func (cp *ControlPlane) emitArtifactReturn(receipt ArtifactReceipt) error {
	return cp.persistArtifactReturn(receipt, "READY")
}

func (cp *ControlPlane) persistArtifactReturn(receipt ArtifactReceipt, state string) error {
	payload := map[string]any{"receipt": receipt}
	return cp.emitReturn(ReturnEnvelope{
		State: state, Schema: "minefield.return/1", ID: "return-" + receipt.ID, Kind: "artifact.result",
		OriginURL: receipt.Origin.OriginURL, ConversationKey: receipt.Origin.ConversationKey,
		TabActorID: receipt.Origin.TabActorID, TabID: receipt.Origin.TabID,
		Payload: payload, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	})
}

func (cp *ControlPlane) emitReturn(env ReturnEnvelope) error {
	if err := cp.ensureArtifactRoots(); err != nil {
		return err
	}
	if env.ID == "" {
		env.ID = "return-" + randomToken(12)
	}
	if env.Schema == "" {
		env.Schema = "minefield.return/1"
	}
	if env.State == "" {
		env.State = "READY"
	}
	if env.CreatedAt == "" {
		env.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	return writeJSONAtomic(filepath.Join(cp.returnPendingRoot(), sanitizeFilename(env.ID)+".json"), env)
}

func (cp *ControlPlane) recoverCommittingReturns() {
	entries, _ := os.ReadDir(cp.returnPendingRoot())
	cp.mu.RLock()
	policy := cp.config.Artifacts
	cp.mu.RUnlock()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			continue
		}
		path := filepath.Join(cp.returnPendingRoot(), entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var env ReturnEnvelope
		if json.Unmarshal(data, &env) != nil || env.State != "COMMITTING" || env.Kind != "artifact.result" {
			continue
		}
		receiptValue, ok := env.Payload["receipt"]
		if !ok {
			env.State = "READY"
			env.Payload = map[string]any{"error": "COMMITTING_RETURN_RECEIPT_MISSING"}
			_ = cp.emitReturn(env)
			continue
		}
		raw, _ := json.Marshal(receiptValue)
		var receipt ArtifactReceipt
		if json.Unmarshal(raw, &receipt) != nil {
			env.State = "READY"
			env.Payload = map[string]any{"error": "COMMITTING_RETURN_RECEIPT_INVALID"}
			_ = cp.emitReturn(env)
			continue
		}
		if receipt.Evidence == nil {
			receipt.Evidence = map[string]any{}
		}
		manifest := filepath.Join(receipt.TargetDirectory, ".minefield-artifact-receipt.json")
		if _, err := os.Stat(manifest); err != nil {
			receipt.Status = "NEEDS_REPAIR"
			receipt.Error = "commit recovery could not verify extracted target receipt"
		} else if policy.DeleteZipAfterExtract {
			if _, err := os.Stat(receipt.ArchivePath); err == nil {
				if err := os.Remove(receipt.ArchivePath); err != nil {
					receipt.Status = "EXTRACTED_ZIP_DELETE_FAILED"
					receipt.Error = err.Error()
				} else {
					receipt.Evidence["zipDeleted"] = true
				}
			} else if errors.Is(err, os.ErrNotExist) {
				receipt.Evidence["zipDeleted"] = true
			} else {
				receipt.Status = "EXTRACTED_ZIP_STAT_FAILED"
				receipt.Error = err.Error()
			}
		}
		receipt.CompletedAt = time.Now().UTC().Format(time.RFC3339Nano)
		env.State = "READY"
		env.Payload = map[string]any{"receipt": receipt, "recoveredFrom": "COMMITTING"}
		_ = cp.emitReturn(env)
		_ = cp.persistArtifactReceipt(receipt)
	}
}

func (cp *ControlPlane) handleReturnPending(w http.ResponseWriter, r *http.Request) {
	if err := cp.authorize(r); err != nil {
		writeHTTPJSON(w, 401, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	cp.recoverCommittingReturns()
	entries, _ := os.ReadDir(cp.returnPendingRoot())
	var returns []ReturnEnvelope
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(cp.returnPendingRoot(), entry.Name()))
		if err != nil {
			continue
		}
		var env ReturnEnvelope
		if json.Unmarshal(data, &env) != nil {
			continue
		}
		if env.State != "" && env.State != "READY" {
			continue
		}
		returns = append(returns, env)
	}
	sort.Slice(returns, func(i, j int) bool { return returns[i].CreatedAt < returns[j].CreatedAt })
	if len(returns) > 100 {
		returns = returns[:100]
	}
	writeHTTPJSON(w, 200, map[string]any{"ok": true, "returns": returns})
}

func (cp *ControlPlane) handleReturnAck(w http.ResponseWriter, r *http.Request) {
	if err := cp.authorize(r); err != nil {
		writeHTTPJSON(w, 401, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	var req struct {
		ID       string `json:"id"`
		Outcome  string `json:"outcome"`
		Evidence any    `json:"evidence"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req) != nil || req.ID == "" {
		writeHTTPJSON(w, 400, map[string]any{"ok": false, "error": "RETURN_ID_REQUIRED"})
		return
	}
	src := filepath.Join(cp.returnPendingRoot(), sanitizeFilename(req.ID)+".json")
	data, err := os.ReadFile(src)
	if err != nil {
		writeHTTPJSON(w, 404, map[string]any{"ok": false, "error": "RETURN_NOT_FOUND"})
		return
	}
	ack := map[string]any{"id": req.ID, "outcome": req.Outcome, "evidence": req.Evidence, "ackedAt": time.Now().UTC().Format(time.RFC3339Nano), "return": json.RawMessage(data)}
	dst := filepath.Join(cp.returnAckRoot(), sanitizeFilename(req.ID)+".json")
	if err := writeJSONAtomic(dst, ack); err != nil {
		writeHTTPJSON(w, 500, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if err := os.Remove(src); err != nil {
		writeHTTPJSON(w, 500, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	cp.recordEvent("return.acked", map[string]any{"id": req.ID, "outcome": req.Outcome})
	writeHTTPJSON(w, 200, map[string]any{"ok": true, "id": req.ID})
}

func (cp *ControlPlane) handleArtifactRegister(w http.ResponseWriter, r *http.Request) {
	if err := cp.authorize(r); err != nil {
		writeHTTPJSON(w, 401, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	var req ArtifactOrigin
	if json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(&req) != nil {
		writeHTTPJSON(w, 400, map[string]any{"ok": false, "error": "INVALID_ORIGIN"})
		return
	}
	if !cp.authorizedOrigin(req.OriginURL) {
		writeHTTPJSON(w, 403, map[string]any{"ok": false, "error": "ORIGIN_NOT_AUTHORIZED"})
		return
	}
	req.Schema = artifactSchema
	req.RegisteredAt = time.Now().UTC().Format(time.RFC3339Nano)
	path, err := cp.saveArtifactOrigin(req)
	if err != nil {
		writeHTTPJSON(w, 500, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeHTTPJSON(w, 200, map[string]any{"ok": true, "path": path})
}

func (cp *ControlPlane) reconcileZipArtifacts(policy ArtifactPolicy) []ArtifactReceipt {
	entries, _ := os.ReadDir(policy.DownloadsRoot)
	var results []ArtifactReceipt
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".zip") {
			continue
		}
		results = append(results, cp.processZipArtifact(filepath.Join(policy.DownloadsRoot, entry.Name()), "", policy))
	}
	return results
}

func hashBytes(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}
