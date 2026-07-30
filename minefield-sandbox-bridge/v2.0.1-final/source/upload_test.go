package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestArtifactPushRoundTripAndOriginBinding(t *testing.T) {
	cp := newTestControlPlane(t)
	downloads := cp.config.Artifacts.DownloadsRoot
	if err := os.MkdirAll(downloads, 0o755); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "payload.zip")
	writeZip(t, archive, map[string]string{"run.cmd": "@echo ok", "dt-run.json": `{"schema":"doubletab/dt-run/1"}`})
	data, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	whole := sha256.Sum256(data)
	origin := cp.config.AuthorizedURLPrefixes[0] + "c/test-conversation"
	uploadID := "upload-test-0001"
	chunks := [][]byte{data[:len(data)/2], data[len(data)/2:]}
	begin := commandEnvelope{ID: "begin", Kind: "artifact", OriginURL: origin, TabID: 7, Payload: map[string]any{
		"action": "push.begin", "uploadId": uploadID, "fileName": "payload.zip", "sha256": hex.EncodeToString(whole[:]), "totalBytes": len(data), "chunkCount": len(chunks), "autoIngest": true,
	}}
	got := cp.executeArtifact(begin, cp.config)
	if ok, _ := got["ok"].(bool); !ok {
		t.Fatalf("begin failed: %#v", got)
	}
	for i, chunk := range chunks {
		sum := sha256.Sum256(chunk)
		env := commandEnvelope{ID: "chunk", Kind: "artifact", OriginURL: origin, TabID: 7, Payload: map[string]any{
			"action": "push.chunk", "uploadId": uploadID, "index": i, "chunkSha256": hex.EncodeToString(sum[:]), "dataBase64": base64.StdEncoding.EncodeToString(chunk),
		}}
		got = cp.executeArtifact(env, cp.config)
		if ok, _ := got["ok"].(bool); !ok {
			t.Fatalf("chunk %d failed: %#v", i, got)
		}
		duplicate := cp.executeArtifact(env, cp.config)
		if duplicate["outcome"] != "UPLOAD_CHUNK_DUPLICATE_SUPPRESSED" {
			t.Fatalf("duplicate not suppressed: %#v", duplicate)
		}
	}
	wrongOrigin := commandEnvelope{ID: "status", Kind: "artifact", OriginURL: origin + "-other", Payload: map[string]any{"action": "push.status", "uploadId": uploadID}}
	rejected := cp.executeArtifact(wrongOrigin, cp.config)
	if rejected["error"] != "UPLOAD_ORIGIN_MISMATCH" {
		t.Fatalf("origin mismatch not rejected: %#v", rejected)
	}
	commit := commandEnvelope{ID: "commit", Kind: "artifact", OriginURL: origin, TabID: 7, Payload: map[string]any{"action": "push.commit", "uploadId": uploadID}}
	got = cp.executeArtifact(commit, cp.config)
	if ok, _ := got["ok"].(bool); !ok {
		t.Fatalf("commit failed: %#v", got)
	}
	if _, err := os.Stat(filepath.Join(downloads, "payload.zip")); !os.IsNotExist(err) {
		t.Fatalf("zip should have been ingested and deleted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(downloads, "payload", "run.cmd")); err != nil {
		t.Fatalf("extracted payload missing: %v", err)
	}
}

func TestArtifactPushRejectsConflictAndMissingChunk(t *testing.T) {
	cp := newTestControlPlane(t)
	origin := cp.config.AuthorizedURLPrefixes[0] + "c/test-conversation"
	data := []byte("not-yet-a-zip")
	whole := sha256.Sum256(data)
	begin := commandEnvelope{OriginURL: origin, Payload: map[string]any{"action": "push.begin", "uploadId": "upload-test-0002", "fileName": "missing.zip", "sha256": hex.EncodeToString(whole[:]), "totalBytes": len(data), "chunkCount": 2, "autoIngest": false}}
	if got := cp.executeArtifact(begin, cp.config); got["ok"] != true {
		t.Fatalf("begin failed: %#v", got)
	}
	sum := sha256.Sum256(data)
	chunk := commandEnvelope{OriginURL: origin, Payload: map[string]any{"action": "push.chunk", "uploadId": "upload-test-0002", "index": 0, "chunkSha256": hex.EncodeToString(sum[:]), "dataBase64": base64.StdEncoding.EncodeToString(data)}}
	if got := cp.executeArtifact(chunk, cp.config); got["ok"] != true {
		t.Fatalf("chunk failed: %#v", got)
	}
	commit := commandEnvelope{OriginURL: origin, Payload: map[string]any{"action": "push.commit", "uploadId": "upload-test-0002"}}
	if got := cp.executeArtifact(commit, cp.config); got["error"] != "UPLOAD_CHUNK_MISSING" {
		t.Fatalf("missing chunk not detected: %#v", got)
	}
}
