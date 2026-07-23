package main

import "testing"

func TestWindMainStatSupport(t *testing.T) {
	if got, ok := ocrDefaultMainStatValue("WIND_DMG"); !ok || got != 30 {
		t.Fatalf("WIND_DMG default main stat = %v, %v; want 30, true", got, ok)
	}
	if slot, ok := uniqueOCRSlotForMainStat("WIND_DMG"); !ok || slot != 5 {
		t.Fatalf("WIND_DMG slot = %d, %v; want 5, true", slot, ok)
	}
	if got := statTypeFromOCRLabel("风属性伤害加成 30%", true); got != "WIND_DMG" {
		t.Fatalf("OCR stat type = %q; want WIND_DMG", got)
	}
	if got := statCNName("WIND_DMG"); got != "风属性伤害" {
		t.Fatalf("Chinese stat name = %q; want 风属性伤害", got)
	}
}

func TestVersion30DriveDiscBonuses(t *testing.T) {
	panel := map[string]float64{}
	applyTwoPiecePanelBonuses(panel, map[string]int{"呼啸沙龙": 2, "拂晓行纪": 2})
	if panel["WIND_DMG"] != 10 || panel["ETHER_DMG"] != 10 {
		t.Fatalf("two-piece bonuses = %#v; want WIND_DMG=10 and ETHER_DMG=10", panel)
	}

	etherPanel := map[string]float64{}
	applyConditionalFourPiecePanelBonuses(etherPanel, map[string]int{"拂晓行纪": 4}, "ETHER")
	if etherPanel["CRIT_DMG"] != 0 {
		t.Fatalf("Dawn 4pc should not affect panel stats in v1.14, got %#v", etherPanel)
	}
	nonEtherPanel := map[string]float64{}
	applyConditionalFourPiecePanelBonuses(nonEtherPanel, map[string]int{"拂晓行纪": 4}, "WIND")
	if nonEtherPanel["CRIT_DMG"] != 0 {
		t.Fatalf("Dawn 4pc non-Ether bonus = %#v; want no panel CRIT_DMG", nonEtherPanel)
	}

	combat := map[string]float64{}
	applyFourPieceCombatBonuses(combat, map[string]int{"呼啸沙龙": 4, "拂晓行纪": 4})
	applyConditionalFourPieceCombatBonuses(combat, map[string]int{"拂晓行纪": 4}, "ETHER")
	if combat["ANOMALY_PROFICIENCY"] != 50 || combat["ELEMENT_DMG"] != 18 || combat["ATK_PERCENT"] != 10 || combat["CRIT_DMG"] != 30 {
		t.Fatalf("4pc combat bonuses = %#v; want AP=50, ELEMENT_DMG=18, ATK_PERCENT=10, CRIT_DMG=30", combat)
	}
	nonEtherCombat := map[string]float64{}
	applyFourPieceCombatBonuses(nonEtherCombat, map[string]int{"拂晓行纪": 4})
	applyConditionalFourPieceCombatBonuses(nonEtherCombat, map[string]int{"拂晓行纪": 4}, "WIND")
	if nonEtherCombat["CRIT_DMG"] != 0 || nonEtherCombat["ATK_PERCENT"] != 10 {
		t.Fatalf("Dawn 4pc non-Ether combat = %#v; want only triggered ATK_PERCENT=10", nonEtherCombat)
	}
}

func TestVelinaCoreConversion(t *testing.T) {
	panel := map[string]float64{"ENERGY_REGEN": 20}
	combat := cloneStatMap(panel)
	req := OptimizeRequest{
		CharacterName:      "维琳娜·艾嘉德",
		BaseEnergyRegen:    1.2,
		BaseAnomalyMastery: 112,
	}
	initial, finalMastery := applyCharacterCombatBonuses(combat, panel, req)
	if !almostEqual(initial, 1.44) {
		t.Fatalf("initial energy regen = %.12f; want 1.44", initial)
	}
	if !almostEqual(combat["VELINA_CORE_DMG_BONUS"], 5.04) {
		t.Fatalf("Velina core damage bonus = %.12f; want 5.04", combat["VELINA_CORE_DMG_BONUS"])
	}
	if !almostEqual(combat["ANOMALY_MASTERY_FLAT"], 12) {
		t.Fatalf("Velina flat anomaly mastery = %.12f; want 12", combat["ANOMALY_MASTERY_FLAT"])
	}
	if !almostEqual(finalMastery, 124) {
		t.Fatalf("final anomaly mastery = %.12f; want 124", finalMastery)
	}

	cappedPanel := map[string]float64{"ENERGY_REGEN": 200}
	cappedCombat := cloneStatMap(cappedPanel)
	_, cappedMastery := applyCharacterCombatBonuses(cappedCombat, cappedPanel, req)
	if cappedCombat["VELINA_CORE_DMG_BONUS"] != 35 || cappedCombat["ANOMALY_MASTERY_FLAT"] != 84 || cappedMastery != 196 {
		t.Fatalf("capped Velina conversion = damage %.3f, flat AM %.3f, final AM %.3f; want 35, 84, 196",
			cappedCombat["VELINA_CORE_DMG_BONUS"], cappedCombat["ANOMALY_MASTERY_FLAT"], cappedMastery)
	}
}

func TestElementAwareDamageBonus(t *testing.T) {
	stats := map[string]float64{
		"WIND_DMG":              10,
		"ETHER_DMG":             99,
		"ELEMENT_DMG":           18,
		"VELINA_CORE_DMG_BONUS": 5.04,
	}
	if got := combatDamageBonusPercent(stats, "WIND"); !almostEqual(got, 33.04) {
		t.Fatalf("Wind damage bonus = %.12f; want 33.04", got)
	}
	if got := combatDamageBonusPercent(stats, "ETHER"); !almostEqual(got, 122.04) {
		t.Fatalf("Ether damage bonus = %.12f; want 122.04", got)
	}
}
