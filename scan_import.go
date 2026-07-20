package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"time"

	scanner "github.com/TonyUB/ZZZ_Drive_Disc/scan"
)

const maxScanImportBytes = 8 << 20

type ScanImportPreview struct {
	PreviewHash string             `json:"previewHash"`
	CapturedAt  time.Time          `json:"capturedAt"`
	AdapterID   string             `json:"adapterId"`
	Region      string             `json:"region"`
	Version     string             `json:"clientVersion"`
	Total       int                `json:"total"`
	Added       int                `json:"added"`
	Updated     int                `json:"updated"`
	Unchanged   int                `json:"unchanged"`
	Changes     []ScanImportChange `json:"changes"`
	Warnings    []string           `json:"warnings,omitempty"`
}

type ScanImportChange struct {
	ID      string `json:"id"`
	Action  string `json:"action"`
	SetName string `json:"setName"`
	Slot    int    `json:"slot"`
}

type ScanImportApplyRequest struct {
	Export      scanner.Export `json:"export"`
	PreviewHash string         `json:"previewHash"`
}

func newScanImportToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func handleScanImportPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !authorizeScanImport(w, r) {
		return
	}
	export, err := decodeScanExport(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	srvState.mu.RLock()
	preview, err := buildScanImportPreview(export, srvState.state)
	srvState.mu.RUnlock()
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, preview)
}

func handleScanImportApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !authorizeScanImport(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxScanImportBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request ScanImportApplyRequest
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid scan apply JSON: "+err.Error())
		return
	}
	if err := ensureJSONEOF(decoder); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateScanExport(request.Export); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(request.PreviewHash) == "" {
		writeError(w, http.StatusBadRequest, "previewHash is required")
		return
	}

	srvState.mu.Lock()
	preview, err := buildScanImportPreview(request.Export, srvState.state)
	if err != nil {
		srvState.mu.Unlock()
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if subtle.ConstantTimeCompare([]byte(preview.PreviewHash), []byte(request.PreviewHash)) != 1 {
		srvState.mu.Unlock()
		writeError(w, http.StatusConflict, "inventory changed after preview; preview the scan again")
		return
	}
	updated := applyScanImport(request.Export, &srvState.state)
	if updated {
		if err := saveState(srvState.storagePath, srvState.state); err != nil {
			srvState.mu.Unlock()
			writeError(w, http.StatusInternalServerError, "failed to save scan import: "+err.Error())
			return
		}
	}
	srvState.mu.Unlock()
	writeJSON(w, preview)
}

func decodeScanExport(w http.ResponseWriter, r *http.Request) (scanner.Export, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxScanImportBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var export scanner.Export
	if err := decoder.Decode(&export); err != nil {
		return scanner.Export{}, fmt.Errorf("invalid scan export JSON: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return scanner.Export{}, err
	}
	if err := validateScanExport(export); err != nil {
		return scanner.Export{}, err
	}
	return export, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("request must contain exactly one JSON document")
	}
	return fmt.Errorf("invalid trailing JSON: %w", err)
}

func validateScanExport(export scanner.Export) error {
	if err := export.Validate(); err != nil {
		return fmt.Errorf("scan export rejected: %w", err)
	}
	if len(export.Discs) > 5000 {
		return fmt.Errorf("scan export contains %d discs; safety limit is 5000", len(export.Discs))
	}
	return nil
}

func authorizeScanImport(w http.ResponseWriter, r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil || !ip.IsLoopback() {
		writeError(w, http.StatusForbidden, "scan import is available only on loopback")
		return false
	}
	if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" {
		parsed, err := url.Parse(origin)
		if err != nil || !strings.EqualFold(parsed.Host, r.Host) {
			writeError(w, http.StatusForbidden, "cross-origin scan import is not allowed")
			return false
		}
	}
	srvState.mu.RLock()
	want := srvState.importToken
	srvState.mu.RUnlock()
	got := r.Header.Get("X-ZZZ-Import-Token")
	if len(want) == 0 || len(got) != len(want) || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		writeError(w, http.StatusForbidden, "invalid scan import token")
		return false
	}
	return true
}

func buildScanImportPreview(export scanner.Export, state AppState) (ScanImportPreview, error) {
	if err := validateScanExport(export); err != nil {
		return ScanImportPreview{}, err
	}
	byID := make(map[string]Disc, len(state.Discs))
	for _, disc := range state.Discs {
		byID[disc.ID] = disc
	}
	preview := ScanImportPreview{
		CapturedAt: export.CapturedAt,
		AdapterID:  export.Protocol.AdapterID,
		Region:     export.Protocol.Region,
		Version:    export.Protocol.ClientVersion,
		Total:      len(export.Discs),
		Changes:    make([]ScanImportChange, 0, len(export.Discs)),
		Warnings:   append([]string(nil), export.Warnings...),
	}
	for _, incoming := range export.Discs {
		action := "unchanged"
		existing, ok := byID[incoming.ID]
		if !ok {
			action = "add"
			preview.Added++
		} else if !scanManagedEqual(existing, incoming) {
			action = "update"
			preview.Updated++
		} else {
			preview.Unchanged++
		}
		preview.Changes = append(preview.Changes, ScanImportChange{ID: incoming.ID, Action: action, SetName: incoming.SetName, Slot: incoming.Slot})
	}
	hash, err := scanPreviewHash(export, state)
	if err != nil {
		return ScanImportPreview{}, err
	}
	preview.PreviewHash = hash
	return preview, nil
}

func scanPreviewHash(export scanner.Export, state AppState) (string, error) {
	managed := make(map[string]any)
	incomingIDs := make(map[string]struct{}, len(export.Discs))
	for _, disc := range export.Discs {
		incomingIDs[disc.ID] = struct{}{}
	}
	for _, disc := range state.Discs {
		if _, ok := incomingIDs[disc.ID]; !ok {
			continue
		}
		managed[disc.ID] = struct {
			SetName  string      `json:"setName"`
			Slot     int         `json:"slot"`
			Rarity   string      `json:"rarity"`
			Level    int         `json:"level"`
			MainStat StatValue   `json:"mainStat"`
			SubStats []StatValue `json:"subStats"`
			Locked   bool        `json:"locked"`
		}{disc.SetName, disc.Slot, disc.Rarity, disc.Level, disc.MainStat, disc.SubStats, disc.Locked}
	}
	payload := struct {
		Export  scanner.Export `json:"export"`
		Managed map[string]any `json:"managed"`
	}{export, managed}
	b, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func scanManagedEqual(existing Disc, incoming scanner.Disc) bool {
	mainStat, subStats := scanStats(incoming)
	return existing.SetName == incoming.SetName && existing.Slot == incoming.Slot &&
		existing.Rarity == incoming.Rarity && existing.Level == incoming.Level &&
		existing.Locked == incoming.Locked && reflect.DeepEqual(existing.MainStat, mainStat) &&
		reflect.DeepEqual(existing.SubStats, subStats)
}

func applyScanImport(export scanner.Export, state *AppState) bool {
	indices := make(map[string]int, len(state.Discs))
	for i := range state.Discs {
		indices[state.Discs[i].ID] = i
	}
	changed := false
	stamp := time.Now().UTC().Format(time.RFC3339)
	for _, incoming := range export.Discs {
		mainStat, subStats := scanStats(incoming)
		stats := append([]StatValue{mainStat}, subStats...)
		if index, ok := indices[incoming.ID]; ok {
			if scanManagedEqual(state.Discs[index], incoming) {
				continue
			}
			existing := &state.Discs[index]
			existing.SetName = incoming.SetName
			existing.Slot = incoming.Slot
			existing.Rarity = incoming.Rarity
			existing.Level = incoming.Level
			existing.Stats = stats
			existing.MainStat = mainStat
			existing.SubStats = subStats
			existing.Locked = incoming.Locked
			existing.UpdatedAt = stamp
			changed = true
			continue
		}
		state.Discs = append(state.Discs, Disc{
			ID: incoming.ID, SetName: incoming.SetName, Slot: incoming.Slot,
			Rarity: incoming.Rarity, Level: incoming.Level, Stats: stats,
			MainStat: mainStat, SubStats: subStats, Locked: incoming.Locked,
			EquippedBy: incoming.EquippedBy, CreatedAt: stamp, UpdatedAt: stamp,
		})
		indices[incoming.ID] = len(state.Discs) - 1
		changed = true
	}
	return changed
}

func scanStats(incoming scanner.Disc) (StatValue, []StatValue) {
	mainStat := StatValue{Type: incoming.MainStat.Type, Value: incoming.MainStat.Value}
	subStats := make([]StatValue, len(incoming.SubStats))
	for i, stat := range incoming.SubStats {
		subStats[i] = StatValue{Type: stat.Type, Value: stat.Value}
	}
	return mainStat, subStats
}
