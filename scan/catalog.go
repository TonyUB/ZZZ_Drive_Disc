package scan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

type Catalog struct {
	ID         string                       `json:"id"`
	AdapterID  string                       `json:"adapterId"`
	Equipment  map[string]EquipmentTemplate `json:"equipment"`
	Properties map[string]PropertyTemplate  `json:"properties"`
}

type EquipmentTemplate struct {
	SetName string `json:"setName"`
	Slot    int    `json:"slot"`
	Rarity  string `json:"rarity"`
}

type PropertyTemplate struct {
	Type     string  `json:"type"`
	Scale    float64 `json:"scale"`
	Decimals int     `json:"decimals"`
}

type EquipmentResponse struct {
	Retcode int32       `json:"retcode"`
	Equips  []EquipInfo `json:"equips"`
}

type EquipInfo struct {
	UID            uint32          `json:"uid"`
	TemplateID     uint32          `json:"templateId"`
	Level          uint32          `json:"level"`
	Star           uint32          `json:"star"`
	Lock           bool            `json:"lock"`
	MainProperties []EquipProperty `json:"mainProperties"`
	SubProperties  []EquipProperty `json:"subProperties"`
}

type EquipProperty struct {
	Key       uint32 `json:"key"`
	BaseValue uint32 `json:"baseValue"`
	AddValue  uint32 `json:"addValue"`
}

func (c Catalog) Validate(adapterID string) error {
	if strings.TrimSpace(c.ID) == "" {
		return errors.New("catalog id is required")
	}
	if c.AdapterID != adapterID {
		return fmt.Errorf("catalog adapterId %q does not match adapter %q", c.AdapterID, adapterID)
	}
	if len(c.Equipment) == 0 || len(c.Properties) == 0 {
		return errors.New("equipment and property catalogs cannot be empty")
	}
	for id, item := range c.Equipment {
		if _, err := strconv.ParseUint(id, 10, 32); err != nil {
			return fmt.Errorf("invalid equipment id %q", id)
		}
		if strings.TrimSpace(item.SetName) == "" || item.Slot < 1 || item.Slot > 6 || strings.TrimSpace(item.Rarity) == "" {
			return fmt.Errorf("equipment %s has invalid setName, slot or rarity", id)
		}
	}
	for id, prop := range c.Properties {
		if _, err := strconv.ParseUint(id, 10, 32); err != nil {
			return fmt.Errorf("invalid property id %q", id)
		}
		if strings.TrimSpace(prop.Type) == "" || prop.Scale <= 0 || prop.Decimals < 0 || prop.Decimals > 6 {
			return fmt.Errorf("property %s has invalid type, scale or decimals", id)
		}
	}
	return nil
}

func (c Catalog) Fingerprint() (string, error) {
	b, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func BuildExport(response EquipmentResponse, adapter Adapter, catalog Catalog, clientVersion, accountUID string, capturedAt time.Time) (Export, error) {
	if err := adapter.Validate(clientVersion); err != nil {
		return Export{}, err
	}
	if err := catalog.Validate(adapter.ID); err != nil {
		return Export{}, err
	}
	if response.Retcode != 0 {
		return Export{}, fmt.Errorf("GetEquipDataScRsp returned retcode %d", response.Retcode)
	}
	if capturedAt.IsZero() {
		capturedAt = time.Now().UTC()
	}
	adapterFingerprint, err := adapter.Fingerprint()
	if err != nil {
		return Export{}, err
	}
	catalogFingerprint, err := catalog.Fingerprint()
	if err != nil {
		return Export{}, err
	}
	out := Export{
		SchemaVersion: SchemaVersion,
		Source:        SourceName,
		CapturedAt:    capturedAt.UTC(),
		Account:       Account{UID: accountUID},
		Protocol: ProtocolInfo{
			AdapterID:     adapter.ID,
			Region:        adapter.Region,
			ClientVersion: clientVersion,
			Fingerprint:   adapterFingerprint + "+catalog:" + strings.TrimPrefix(catalogFingerprint, "sha256:"),
			Verified:      true,
		},
		Discs: make([]Disc, 0, len(response.Equips)),
	}
	seen := make(map[uint32]struct{}, len(response.Equips))
	for _, equip := range response.Equips {
		if equip.UID == 0 {
			return Export{}, errors.New("equipment response contains a zero uid")
		}
		if _, ok := seen[equip.UID]; ok {
			return Export{}, fmt.Errorf("equipment response contains duplicate uid %d", equip.UID)
		}
		seen[equip.UID] = struct{}{}
		template, ok := catalog.Equipment[strconv.FormatUint(uint64(equip.TemplateID), 10)]
		if !ok {
			return Export{}, fmt.Errorf("unknown equipment template %d; refusing a partial import", equip.TemplateID)
		}
		if len(equip.MainProperties) != 1 {
			return Export{}, fmt.Errorf("equipment %d has %d main properties, want exactly 1", equip.UID, len(equip.MainProperties))
		}
		mainStat, err := catalog.convertProperty(equip.MainProperties[0])
		if err != nil {
			return Export{}, fmt.Errorf("equipment %d main property: %w", equip.UID, err)
		}
		subs := make([]Stat, 0, len(equip.SubProperties))
		for _, property := range equip.SubProperties {
			stat, err := catalog.convertProperty(property)
			if err != nil {
				return Export{}, fmt.Errorf("equipment %d sub property: %w", equip.UID, err)
			}
			subs = append(subs, stat)
		}
		disc := Disc{
			ID:         "zzz:" + strconv.FormatUint(uint64(equip.UID), 10),
			TemplateID: equip.TemplateID,
			SetName:    template.SetName,
			Slot:       template.Slot,
			Rarity:     template.Rarity,
			Level:      int(equip.Level),
			Locked:     equip.Lock,
			MainStat:   mainStat,
			SubStats:   subs,
		}
		if err := disc.Validate(); err != nil {
			return Export{}, fmt.Errorf("equipment %d: %w", equip.UID, err)
		}
		out.Discs = append(out.Discs, disc)
	}
	if err := out.Validate(); err != nil {
		return Export{}, err
	}
	return out, nil
}

func (c Catalog) convertProperty(property EquipProperty) (Stat, error) {
	template, ok := c.Properties[strconv.FormatUint(uint64(property.Key), 10)]
	if !ok {
		return Stat{}, fmt.Errorf("unknown property key %d", property.Key)
	}
	value := float64(uint64(property.BaseValue)+uint64(property.AddValue)) * template.Scale
	pow := math.Pow10(template.Decimals)
	value = math.Round(value*pow) / pow
	return Stat{Type: template.Type, Value: value, PropertyID: property.Key}, nil
}
