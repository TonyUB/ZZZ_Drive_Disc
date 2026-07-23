package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadExternalScannerExportConvertsChineseStats(t *testing.T) {
	path := filepath.Join(t.TempDir(), "export.json")
	data := `[
  {
    "序号": 1,
    "名称": "呼啸沙龙",
    "槽位": 5,
    "品质": "S",
    "等级": 15,
    "最大等级": 15,
    "主属性": {"风属性伤害加成": "30%"},
    "副属性": [
      {"暴击率": "4.8%"},
      {"暴击伤害": "9.6%"},
      {"攻击力": "6%"},
      {"穿透值": 18}
    ]
  }
]`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	discs, err := readExternalScannerExport(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(discs) != 1 {
		t.Fatalf("got %d discs; want 1", len(discs))
	}
	got := discs[0]
	if got.SetName != "呼啸沙龙" || got.Slot != 5 || got.MainStat.Type != "WIND_DMG" || got.MainStat.Value != 30 {
		t.Fatalf("unexpected converted disc: %#v", got)
	}
	want := []StatValue{
		{Type: "CRIT_RATE", Value: 4.8},
		{Type: "CRIT_DMG", Value: 9.6},
		{Type: "ATK_PERCENT", Value: 6},
		{Type: "PEN_FLAT", Value: 18},
	}
	for i := range want {
		if got.SubStats[i].Type != want[i].Type || !almostEqual(got.SubStats[i].Value, want[i].Value) {
			t.Fatalf("sub stat %d = %#v; want %#v", i, got.SubStats[i], want[i])
		}
	}
}

func TestExternalScannerPreviewPreservesMatchedMetadataAndReplacesOnlyScannerDiscs(t *testing.T) {
	matched := Disc{
		ID: "ocrscan:matched", SetName: "啄木鸟电音", Slot: 4, Rarity: "S", Level: 15,
		MainStat: StatValue{Type: "CRIT_RATE", Value: 24},
		SubStats: []StatValue{{Type: "CRIT_DMG", Value: 9.6}},
		Locked:   true, Discarded: true, EquippedBy: "安比", Note: "保留备注",
		CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:00Z",
	}
	matched.Stats = append([]StatValue{matched.MainStat}, matched.SubStats...)
	stale := Disc{
		ID: "ocrscan:stale", SetName: "自由蓝调", Slot: 1, Rarity: "S", Level: 15,
		MainStat: StatValue{Type: "HP_FLAT", Value: 2200},
	}
	manual := Disc{ID: "disc_manual", SetName: "手工记录", Slot: 2, Rarity: "S", Level: 15, MainStat: StatValue{Type: "ATK_FLAT", Value: 316}}
	state := AppState{
		Version:         appVersion,
		Discs:           []Disc{manual, matched, stale},
		CharacterBuilds: []CharacterBuild{{ID: "build", DiscIDs: []string{matched.ID, stale.ID}}},
		DiscClaims:      []DiscClaim{{DiscID: matched.ID}, {DiscID: stale.ID}},
	}
	newDisc := Disc{
		SetName: "呼啸沙龙", Slot: 5, Rarity: "S", Level: 15,
		MainStat: StatValue{Type: "WIND_DMG", Value: 30},
		SubStats: []StatValue{{Type: "CRIT_RATE", Value: 4.8}},
	}
	newDisc.Stats = append([]StatValue{newDisc.MainStat}, newDisc.SubStats...)
	source := []Disc{matched, newDisc}
	for i := range source {
		source[i].ID = ""
		source[i].Locked = false
		source[i].Discarded = false
		source[i].EquippedBy = ""
		source[i].Note = ""
		source[i].CreatedAt = ""
		source[i].UpdatedAt = ""
	}

	preview, applyDiscs, err := buildExternalScannerPreview(source, state)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Total != 2 || preview.Added != 1 || preview.Removed != 1 || preview.Unchanged != 1 || preview.PreviewHash == "" {
		t.Fatalf("unexpected preview: %#v", preview)
	}
	if applyDiscs[0].ID != matched.ID || !applyDiscs[0].Locked || !applyDiscs[0].Discarded || applyDiscs[0].EquippedBy != "安比" || applyDiscs[0].Note != "保留备注" {
		t.Fatalf("matched metadata was not preserved: %#v", applyDiscs[0])
	}
	if len(applyDiscs[1].ID) <= len(externalScannerDiscIDPrefix) || applyDiscs[1].ID[:len(externalScannerDiscIDPrefix)] != externalScannerDiscIDPrefix {
		t.Fatalf("new scanner ID = %q", applyDiscs[1].ID)
	}

	applyExternalScannerDiscs(&state, applyDiscs)
	if len(state.Discs) != 3 || state.Discs[0].ID != manual.ID {
		t.Fatalf("manual inventory was not preserved: %#v", state.Discs)
	}
	if len(state.DiscClaims) != 1 || state.DiscClaims[0].DiscID != matched.ID {
		t.Fatalf("stale claims were not cleaned: %#v", state.DiscClaims)
	}
	if len(state.CharacterBuilds[0].DiscIDs) != 1 || state.CharacterBuilds[0].DiscIDs[0] != matched.ID {
		t.Fatalf("stale build references were not cleaned: %#v", state.CharacterBuilds[0].DiscIDs)
	}
}

func TestScannerOutputValueUsesLastOccurrence(t *testing.T) {
	output := []byte("export_file=C:\\old\\export.json\r\nprogress=working\r\nexport_file=C:\\new\\export.json\r\n")
	if got := scannerOutputValue(output, "export_file"); got != `C:\new\export.json` {
		t.Fatalf("export path = %q", got)
	}
}

func TestPartialScannerExportIsNotACompleteResult(t *testing.T) {
	if isCompleteExternalScannerExport(filepath.Join("scan", "export.partial.json")) {
		t.Fatal("partial scanner export must not be accepted as a complete result")
	}
	if !isCompleteExternalScannerExport(filepath.Join("scan", "export.json")) {
		t.Fatal("complete scanner export should be accepted")
	}
}
