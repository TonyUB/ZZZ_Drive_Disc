package scan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Adapter describes only the protobuf layout needed to decode
// GetEquipDataScRsp. Authentication and transport remain separate so a
// developer can update one game version without changing the export schema.
type Adapter struct {
	ID             string         `json:"id"`
	Region         string         `json:"region"`
	ClientVersions []string       `json:"clientVersions"`
	Command        CommandLayout  `json:"command"`
	Response       ResponseLayout `json:"response"`
	Equip          EquipLayout    `json:"equip"`
	Property       PropertyLayout `json:"property"`
}

type CommandLayout struct {
	GetEquipDataRequest  uint16 `json:"getEquipDataRequest"`
	GetEquipDataResponse uint16 `json:"getEquipDataResponse"`
}

type ResponseLayout struct {
	Retcode   ScalarField `json:"retcode"`
	EquipList int         `json:"equipList"`
}

type EquipLayout struct {
	UID            ScalarField `json:"uid"`
	TemplateID     ScalarField `json:"templateId"`
	Level          ScalarField `json:"level"`
	Star           ScalarField `json:"star"`
	Lock           ScalarField `json:"lock"`
	MainProperties int         `json:"mainProperties"`
	SubProperties  int         `json:"subProperties"`
}

type PropertyLayout struct {
	Key       ScalarField `json:"key"`
	BaseValue ScalarField `json:"baseValue"`
	AddValue  ScalarField `json:"addValue"`
}

type ScalarField struct {
	Number int    `json:"number"`
	XOR    uint64 `json:"xor,omitempty"`
}

func (a Adapter) Validate(clientVersion string) error {
	if strings.TrimSpace(a.ID) == "" || strings.TrimSpace(a.Region) == "" {
		return errors.New("adapter id and region are required")
	}
	if !slices.Contains(a.ClientVersions, clientVersion) {
		return fmt.Errorf("adapter %q does not explicitly support client version %q", a.ID, clientVersion)
	}
	if a.Command.GetEquipDataRequest == 0 || a.Command.GetEquipDataResponse == 0 {
		return errors.New("GetEquipData command ids are required")
	}
	fields := []struct {
		name string
		num  int
	}{
		{"response.retcode", a.Response.Retcode.Number},
		{"response.equipList", a.Response.EquipList},
		{"equip.uid", a.Equip.UID.Number},
		{"equip.templateId", a.Equip.TemplateID.Number},
		{"equip.level", a.Equip.Level.Number},
		{"equip.star", a.Equip.Star.Number},
		{"equip.lock", a.Equip.Lock.Number},
		{"equip.mainProperties", a.Equip.MainProperties},
		{"equip.subProperties", a.Equip.SubProperties},
		{"property.key", a.Property.Key.Number},
		{"property.baseValue", a.Property.BaseValue.Number},
		{"property.addValue", a.Property.AddValue.Number},
	}
	for _, field := range fields {
		if field.num <= 0 {
			return fmt.Errorf("%s must be a positive protobuf field number", field.name)
		}
	}
	return nil
}

// Fingerprint is deterministic and changes whenever a protocol field, command
// id, region or supported client version changes.
func (a Adapter) Fingerprint() (string, error) {
	b, err := json.Marshal(a)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
