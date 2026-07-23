// Package scan contains the reusable, game-version-independent part of the
// ZZZ drive-disc scanner. Version-specific login and protocol code is supplied
// by an Adapter; exports use a stable schema that optimizers can validate.
package scan

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	SchemaVersion = 1
	SourceName    = "zzz-drive-scan"
)

// Export is the only document format that the optimizer accepts from a
// scanner. Login tokens, session keys and raw network packets intentionally do
// not have fields in this schema.
type Export struct {
	SchemaVersion int          `json:"schemaVersion"`
	Source        string       `json:"source"`
	CapturedAt    time.Time    `json:"capturedAt"`
	Account       Account      `json:"account"`
	Protocol      ProtocolInfo `json:"protocol"`
	Discs         []Disc       `json:"discs"`
	Warnings      []string     `json:"warnings,omitempty"`
}

type Account struct {
	// UID is optional and should be omitted by callers that do not want account
	// metadata in the exported file.
	UID string `json:"uid,omitempty"`
}

type ProtocolInfo struct {
	AdapterID     string `json:"adapterId"`
	Region        string `json:"region"`
	ClientVersion string `json:"clientVersion"`
	Fingerprint   string `json:"fingerprint"`
	// Verified is true only when the adapter explicitly lists ClientVersion and
	// its protocol/catalog fingerprints have passed validation.
	Verified bool `json:"verified"`
}

type Disc struct {
	// ID is derived from the in-game EquipInfo.uid and remains stable across
	// repeated scans. It must not contain a login or account token.
	ID         string `json:"id"`
	TemplateID uint32 `json:"templateId"`
	SetName    string `json:"setName"`
	Slot       int    `json:"slot"`
	Rarity     string `json:"rarity"`
	Level      int    `json:"level"`
	Locked     bool   `json:"locked"`
	EquippedBy string `json:"equippedBy,omitempty"`
	MainStat   Stat   `json:"mainStat"`
	SubStats   []Stat `json:"subStats"`
}

type Stat struct {
	Type       string  `json:"type"`
	Value      float64 `json:"value"`
	PropertyID uint32  `json:"propertyId,omitempty"`
}

func (e Export) Validate() error {
	if e.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported scan schema version %d", e.SchemaVersion)
	}
	if e.Source != SourceName {
		return fmt.Errorf("unexpected scan source %q", e.Source)
	}
	if e.CapturedAt.IsZero() {
		return errors.New("capturedAt is required")
	}
	if err := e.Protocol.Validate(); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(e.Discs))
	for i, disc := range e.Discs {
		if err := disc.Validate(); err != nil {
			return fmt.Errorf("disc %d: %w", i, err)
		}
		if _, ok := seen[disc.ID]; ok {
			return fmt.Errorf("disc %d: duplicate id %q", i, disc.ID)
		}
		seen[disc.ID] = struct{}{}
	}
	return nil
}

func (p ProtocolInfo) Validate() error {
	if strings.TrimSpace(p.AdapterID) == "" || strings.TrimSpace(p.Region) == "" ||
		strings.TrimSpace(p.ClientVersion) == "" || strings.TrimSpace(p.Fingerprint) == "" {
		return errors.New("adapterId, region, clientVersion and fingerprint are required")
	}
	if !p.Verified {
		return errors.New("protocol adapter is not verified for this client version")
	}
	return nil
}

func (d Disc) Validate() error {
	if !strings.HasPrefix(d.ID, "zzz:") || len(d.ID) <= len("zzz:") {
		return errors.New("id must be derived from EquipInfo.uid and use the zzz:<uid> form")
	}
	if d.TemplateID == 0 {
		return errors.New("templateId is required")
	}
	if strings.TrimSpace(d.SetName) == "" {
		return errors.New("setName is required")
	}
	if d.Slot < 1 || d.Slot > 6 {
		return fmt.Errorf("slot must be between 1 and 6, got %d", d.Slot)
	}
	if strings.TrimSpace(d.Rarity) == "" {
		return errors.New("rarity is required")
	}
	if d.Level < 0 {
		return errors.New("level cannot be negative")
	}
	if err := d.MainStat.Validate(); err != nil {
		return fmt.Errorf("main stat: %w", err)
	}
	if len(d.SubStats) > 4 {
		return fmt.Errorf("at most 4 sub stats are allowed, got %d", len(d.SubStats))
	}
	for i, stat := range d.SubStats {
		if err := stat.Validate(); err != nil {
			return fmt.Errorf("sub stat %d: %w", i, err)
		}
	}
	return nil
}

func (s Stat) Validate() error {
	if strings.TrimSpace(s.Type) == "" {
		return errors.New("type is required")
	}
	if s.Value < 0 {
		return errors.New("value cannot be negative")
	}
	return nil
}
