package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	scanner "github.com/TonyUB/ZZZ_Drive_Disc/scan"
)

func TestScanImportHTTPPreviewRequiresTokenAndApplies(t *testing.T) {
	oldAppState, oldStoragePath, oldImportToken := srvState.state, srvState.storagePath, srvState.importToken
	defer func() {
		srvState = serverState{state: oldAppState, storagePath: oldStoragePath, importToken: oldImportToken}
	}()
	srvState = serverState{
		state:       AppState{Version: appVersion, Discs: []Disc{}},
		storagePath: filepath.Join(t.TempDir(), "state.json"),
		importToken: "test-import-token",
	}
	export := validScanExport([]scanner.Disc{{
		ID: "zzz:10", TemplateID: 31001, SetName: "套装", Slot: 1, Rarity: "S", Level: 15,
		MainStat: scanner.Stat{Type: "HP_FLAT", Value: 2200}, SubStats: []scanner.Stat{},
	}})
	exportJSON, err := json.Marshal(export)
	if err != nil {
		t.Fatal(err)
	}

	unauthorized := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/scan/import/preview", bytes.NewReader(exportJSON))
	unauthorized.RemoteAddr = "127.0.0.1:12345"
	unauthorizedRecorder := httptest.NewRecorder()
	handleScanImportPreview(unauthorizedRecorder, unauthorized)
	if unauthorizedRecorder.Code != http.StatusForbidden {
		t.Fatalf("missing token: got status %d", unauthorizedRecorder.Code)
	}

	previewRequest := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/scan/import/preview", bytes.NewReader(exportJSON))
	previewRequest.RemoteAddr = "127.0.0.1:12345"
	previewRequest.Header.Set("X-ZZZ-Import-Token", "test-import-token")
	previewRecorder := httptest.NewRecorder()
	handleScanImportPreview(previewRecorder, previewRequest)
	if previewRecorder.Code != http.StatusOK {
		t.Fatalf("preview status %d: %s", previewRecorder.Code, previewRecorder.Body.String())
	}
	var preview ScanImportPreview
	if err := json.Unmarshal(previewRecorder.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}

	applyBody, err := json.Marshal(ScanImportApplyRequest{Export: export, PreviewHash: preview.PreviewHash})
	if err != nil {
		t.Fatal(err)
	}
	applyRequest := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/scan/import/apply", bytes.NewReader(applyBody))
	applyRequest.RemoteAddr = "127.0.0.1:12345"
	applyRequest.Header.Set("X-ZZZ-Import-Token", "test-import-token")
	applyRecorder := httptest.NewRecorder()
	handleScanImportApply(applyRecorder, applyRequest)
	if applyRecorder.Code != http.StatusOK {
		t.Fatalf("apply status %d: %s", applyRecorder.Code, applyRecorder.Body.String())
	}
	if len(srvState.state.Discs) != 1 || srvState.state.Discs[0].ID != "zzz:10" {
		t.Fatalf("scan import was not applied: %#v", srvState.state.Discs)
	}
}

func TestScanImportPreviewAndApplyUpsert(t *testing.T) {
	existing := Disc{
		ID: "zzz:1", SetName: "旧套装", Slot: 1, Rarity: "S", Level: 12,
		MainStat: StatValue{Type: "HP_FLAT", Value: 2000},
		SubStats: []StatValue{{Type: "CRIT_RATE", Value: 2.4}},
		Locked:   false, Discarded: true, EquippedBy: "安比", Note: "保留备注",
		CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:00Z",
	}
	state := AppState{Version: appVersion, Discs: []Disc{existing}}
	export := validScanExport([]scanner.Disc{
		{
			ID: "zzz:1", TemplateID: 31001, SetName: "新套装", Slot: 2, Rarity: "S", Level: 15, Locked: true,
			MainStat: scanner.Stat{Type: "ATK_FLAT", Value: 316},
			SubStats: []scanner.Stat{{Type: "CRIT_RATE", Value: 4.8}},
		},
		{
			ID: "zzz:2", TemplateID: 31002, SetName: "新套装", Slot: 3, Rarity: "S", Level: 0,
			MainStat: scanner.Stat{Type: "DEF_FLAT", Value: 0}, SubStats: []scanner.Stat{},
		},
	})

	preview, err := buildScanImportPreview(export, state)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Added != 1 || preview.Updated != 1 || preview.Unchanged != 0 || preview.PreviewHash == "" {
		t.Fatalf("unexpected preview: %#v", preview)
	}
	if !applyScanImport(export, &state) {
		t.Fatal("expected import to modify state")
	}
	if len(state.Discs) != 2 {
		t.Fatalf("expected 2 discs, got %d", len(state.Discs))
	}
	got := state.Discs[0]
	if got.SetName != "新套装" || got.Slot != 2 || !got.Locked || got.Level != 15 {
		t.Fatalf("managed fields not updated: %#v", got)
	}
	if !got.Discarded || got.EquippedBy != "安比" || got.Note != "保留备注" || got.CreatedAt != existing.CreatedAt {
		t.Fatalf("user-managed fields were not preserved: %#v", got)
	}
	if applyScanImport(export, &state) {
		t.Fatal("repeated import must be idempotent")
	}
	second, err := buildScanImportPreview(export, state)
	if err != nil {
		t.Fatal(err)
	}
	if second.Unchanged != 2 || second.Added != 0 || second.Updated != 0 {
		t.Fatalf("unexpected repeated preview: %#v", second)
	}
}

func TestScanPreviewHashChangesWithManagedInventory(t *testing.T) {
	export := validScanExport([]scanner.Disc{{
		ID: "zzz:1", TemplateID: 31001, SetName: "套装", Slot: 1, Rarity: "S", Level: 15,
		MainStat: scanner.Stat{Type: "HP_FLAT", Value: 2200}, SubStats: []scanner.Stat{},
	}})
	state := AppState{Discs: []Disc{{
		ID: "zzz:1", SetName: "套装", Slot: 1, Rarity: "S", Level: 15,
		MainStat: StatValue{Type: "HP_FLAT", Value: 2200}, SubStats: []StatValue{},
	}}}
	first, err := scanPreviewHash(export, state)
	if err != nil {
		t.Fatal(err)
	}
	state.Discs[0].Level = 14
	second, err := scanPreviewHash(export, state)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("preview hash must change when scanner-managed inventory changes")
	}
}

func validScanExport(discs []scanner.Disc) scanner.Export {
	return scanner.Export{
		SchemaVersion: scanner.SchemaVersion,
		Source:        scanner.SourceName,
		CapturedAt:    time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC),
		Protocol: scanner.ProtocolInfo{
			AdapterID: "test", Region: "cn", ClientVersion: "test-1",
			Fingerprint: "sha256:test", Verified: true,
		},
		Discs: discs,
	}
}
