package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEmbeddedInventoryAssets(t *testing.T) {
	for _, path := range []string{
		"web/index.html",
		"web/assets/asset-map.js",
		"web/assets/drive-disc-interop.js",
		"web/assets/drive-discs/drive-disc-01.png",
		"web/assets/drive-discs/drive-disc-29.png",
		"web/assets/drive-discs/drive-disc-30.png",
		"web/assets/agents/agent-01.png",
		"web/assets/agents/agent-57.png",
		"web/assets/agents/Q_AVATAR_SOURCE_MANIFEST.json",
		"web/assets/ASSET_SOURCES.md",
	} {
		data, err := webFiles.ReadFile(path)
		if err != nil {
			t.Fatalf("embedded asset %q: %v", path, err)
		}
		if len(data) == 0 {
			t.Fatalf("embedded asset %q is empty", path)
		}
	}
}

func TestInteropFieldsSurviveStateRoundTrip(t *testing.T) {
	raw := []byte(`{
		"id":"scanner-abc","setName":"流光咏叹","setId":"astral_voice","slot":1,
		"rarity":"S","level":15,"maxLevel":15,"locked":false,"discarded":false,
		"equippedBy":"","note":"","createdAt":"2026-07-28T00:00:00Z","updatedAt":"2026-07-28T00:00:00Z",
		"source":{"type":"zzz-scanner"},"reservedForAgentId":"agent-a","futureField":{"keep":true},
		"mainStat":{"type":"HP_FLAT","stat":"hpFlat","mode":"flat","value":2200,"label":"生命值","rawValue":2200},
		"subStats":[{"type":"CRIT_RATE","stat":"critRate","mode":"pct","value":4.8,"label":"暴击率","rawValue":"4.8%"}]
	}`)
	var disc Disc
	if err := json.Unmarshal(raw, &disc); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(disc)
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range [][]byte{
		[]byte(`"setId":"astral_voice"`), []byte(`"maxLevel":15`),
		[]byte(`"reservedForAgentId":"agent-a"`), []byte(`"futureField":{"keep":true}`),
		[]byte(`"stat":"hpFlat"`), []byte(`"rawValue":"4.8%"`),
	} {
		if !bytes.Contains(encoded, marker) {
			t.Fatalf("round trip lost interoperability field %s: %s", marker, encoded)
		}
	}
}

func TestBundledScannerIntegrity(t *testing.T) {
	root, err := findScannerBundle()
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := verifyScannerBundle(root)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Version != "1.0.45" || manifest.ReleaseTag != "scanner-1.0.45" {
		t.Fatalf("unexpected bundled scanner metadata: %#v", manifest)
	}
	for _, required := range []string{
		"ZZZ-Scanner.Next.exe",
		"Resources/models/PP-OCRv5_mobile_rec_infer.onnx",
		"Data/drive_discs.json",
		"onnxruntime.dll",
	} {
		if manifest.Files[required] == "" {
			t.Fatalf("scanner manifest is missing %s", required)
		}
	}
}

func TestScannerBundlePathCannotEscapeRoot(t *testing.T) {
	if _, err := scannerBundleFile(t.TempDir(), "../outside.exe"); err == nil {
		t.Fatal("scanner bundle path traversal should be rejected")
	}
}

func TestDualReleaseRendering(t *testing.T) {
	index, err := webFiles.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	versionA := renderIndexPage(index, "A")
	versionB := renderIndexPage(index, "B")
	if !bytes.Contains(versionA, []byte("v1.02A")) || !bytes.Contains(versionB, []byte("v1.02B")) {
		t.Fatal("release label was not rendered for both editions")
	}
	if bytes.Contains(versionA, []byte(`id="startScannerBtn"`)) || bytes.Contains(versionA, []byte(`>打开驱动盘扫描器</button>`)) {
		t.Fatal("V1.02A must not render the scanner button")
	}
	if !bytes.Contains(versionA, []byte("const SCANNER_AVAILABLE=false")) {
		t.Fatal("V1.02A must disable scanner JavaScript")
	}
	if !scannerIncludedForEdition("B") || scannerIncludedForEdition("A") {
		t.Fatal("scanner edition selection is incorrect")
	}
	for _, marker := range []string{
		`id="startScannerBtn"`,
		`class="scannerLaunchButton"`,
		`>打开驱动盘扫描器</button>`,
		`点击“检测窗口”→“开始扫描”`,
		`/api/scanner/start`,
		`startBundledScanner`,
		`background: #16a34a`,
	} {
		if !bytes.Contains(versionB, []byte(marker)) {
			t.Fatalf("scanner integration marker missing: %s", marker)
		}
	}
	inputSection := bytes.Index(versionB, []byte(`<section class="card" id="input">`))
	button := bytes.Index(versionB, []byte(`id="startScannerBtn"`))
	tip := bytes.Index(versionB, []byte(`<div class="tipText">`))
	if inputSection < 0 || button <= inputSection || tip <= button {
		t.Fatalf("scanner button should be the first control in the drive-disc input section: section=%d button=%d tip=%d", inputSection, button, tip)
	}
}

func anomalyPanelDiscs() []Disc {
	return []Disc{
		testDisc("谶羽之誓", 1, sv("HP_FLAT", 2200)),
		testDisc("谶羽之誓", 2, sv("ATK_FLAT", 316)),
		testDisc("谶羽之誓", 3, sv("DEF_FLAT", 184)),
		testDisc("谶羽之誓", 4, sv("ANOMALY_PROFICIENCY", 92)),
		testDisc("自由蓝调", 5, sv("ATK_PERCENT", 30)),
		testDisc("自由蓝调", 6, sv("ANOMALY_MASTERY", 30)),
	}
}

func anomalyPanelRequest() OptimizeRequest {
	return OptimizeRequest{
		RoleSystem:       "ANOMALY",
		Mode:             "ANOMALY_AP",
		CharacterName:    "蕾米埃尔·丹",
		CharacterElement: "LUMIFLUX",
		SetPattern:       "4+2",
		Required4Set:     "谶羽之誓",
		Required2Set:     "自由蓝调",
		BaseATK:          823,
		BaseCritRate:     5,
		BaseCritDmg:      50,
		ExtraStats: map[string]float64{
			"BASE_ATK":            743,
			"ATK_PERCENT":         36,
			"ANOMALY_PROFICIENCY": 224,
		},
		CombatExtraStats: map[string]float64{
			"ANOMALY_PROFICIENCY": 96,
			"ATK_PERCENT":         20,
		},
		WantedWeights: roleEffectiveWeights("ANOMALY", "ANOMALY_AP", nil),
	}
}

func TestAnomalyResultUsesAgentPanelValues(t *testing.T) {
	req := anomalyPanelRequest()
	res, ok := evaluateBuild(anomalyPanelDiscs(), req, nil)
	if !ok {
		t.Fatal("anomaly panel fixture did not evaluate")
	}
	if res.Stats["ANOMALY_PROFICIENCY"] != 376 {
		t.Fatalf("panel AP = %.0f; want 376", res.Stats["ANOMALY_PROFICIENCY"])
	}
	if res.CombatStats["ANOMALY_PROFICIENCY"] != 522 {
		t.Fatalf("combat AP = %.0f; want 522", res.CombatStats["ANOMALY_PROFICIENCY"])
	}
	if res.FinalAttack != 2915 || res.CombatFinalAttack != 3228 {
		t.Fatalf("panel/combat attack = %.0f/%.0f; want 2915/3228", res.FinalAttack, res.CombatFinalAttack)
	}
	if !strings.Contains(res.Reason, "面板异常精通 376.0") || !strings.Contains(res.Reason, "面板攻击 2915") {
		t.Fatalf("anomaly reason does not use panel values: %s", res.Reason)
	}
	if strings.Contains(res.Reason, "522.0") || strings.Contains(res.Reason, "攻击力 86.0%") {
		t.Fatalf("anomaly reason leaked combat/component values: %s", res.Reason)
	}
}

func TestAnomalyTargetCannotBeSatisfiedByCombatBonus(t *testing.T) {
	req := anomalyPanelRequest()
	req.TargetAnomalyProficiency = 450
	if _, ok := evaluateBuild(anomalyPanelDiscs(), req, nil); ok {
		t.Fatal("combat AP must not satisfy a panel-facing AP target")
	}
}

func TestAnomalyAttackModeSortsByFinalPanelAttack(t *testing.T) {
	results := []OptimizeResult{
		{FinalAttack: 2800, Stats: map[string]float64{"ATK_PERCENT": 90, "ANOMALY_PROFICIENCY": 400}},
		{FinalAttack: 3000, Stats: map[string]float64{"ATK_PERCENT": 80, "ANOMALY_PROFICIENCY": 400}},
	}
	sortResults(results, "ANOMALY_ATK")
	if results[0].FinalAttack != 3000 {
		t.Fatalf("ANOMALY_ATK sorted by a component instead of final panel attack: %#v", results)
	}
}

func TestAnomalyResultMarkupUsesPanelStats(t *testing.T) {
	index, err := webFiles.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"const panelStats=res.stats||{}", "面板异常精通", "面板攻击"} {
		if !bytes.Contains(index, []byte(marker)) {
			t.Fatalf("anomaly result panel marker missing: %s", marker)
		}
	}
	if bytes.Contains(index, []byte("const st=res.combatStats||res.stats||{}")) {
		t.Fatal("anomaly result panel still reads combatStats")
	}
}

func remielleScreenshotDiscs() []Disc {
	return []Disc{
		testDisc("谶羽之誓", 1, sv("HP_FLAT", 2200), sv("ATK_PERCENT", 3), sv("ANOMALY_PROFICIENCY", 36), sv("ATK_FLAT", 38), sv("PEN_FLAT", 9)),
		testDisc("静听嘉音", 2, sv("ATK_FLAT", 316), sv("HP_PERCENT", 6), sv("ANOMALY_PROFICIENCY", 27), sv("DEF_PERCENT", 9.6), sv("ATK_PERCENT", 6)),
		testDisc("谶羽之誓", 3, sv("DEF_FLAT", 184), sv("PEN_FLAT", 9), sv("CRIT_DMG", 4.8), sv("ANOMALY_PROFICIENCY", 27), sv("ATK_PERCENT", 9)),
		testDisc("静听嘉音", 4, sv("ANOMALY_PROFICIENCY", 92), sv("ATK_PERCENT", 9), sv("CRIT_DMG", 4.8), sv("PEN_FLAT", 18), sv("DEF_FLAT", 30)),
		testDisc("谶羽之誓", 5, sv("ATK_PERCENT", 30), sv("ATK_FLAT", 38), sv("PEN_FLAT", 18), sv("ANOMALY_PROFICIENCY", 27), sv("CRIT_DMG", 4.8)),
		testDisc("谶羽之誓", 6, sv("ATK_PERCENT", 30), sv("ANOMALY_PROFICIENCY", 27), sv("ATK_FLAT", 38), sv("CRIT_RATE", 4.8), sv("DEF_FLAT", 30)),
	}
}

func remielleScreenshotRequest() OptimizeRequest {
	return OptimizeRequest{
		Discs:                    remielleScreenshotDiscs(),
		RoleSystem:               "ANOMALY",
		Mode:                     "ANOMALY_AP",
		CharacterName:            "蕾米埃尔·丹",
		CharacterElement:         "LUMIFLUX",
		SetPattern:               "4+2",
		Required4Set:             "谶羽之誓",
		Required2Set:             "静听嘉音",
		BaseHP:                   7482,
		BaseATK:                  823,
		BaseDEF:                  600,
		BaseCritRate:             5,
		BaseCritDmg:              50,
		BaseAnomalyMastery:       115,
		BaseEnergyRegen:          1.2,
		TargetFinalAttack:        4078,
		TargetAnomalyProficiency: 436,
		TopN:                     20,
		TopKPerSlot:              80,
		MaxCombinations:          2000000,
		ExtraStats: map[string]float64{
			"BASE_ATK":            743,
			"ATK_PERCENT":         36,
			"ANOMALY_PROFICIENCY": 170,
		},
		CombatExtraStats: map[string]float64{
			"ANOMALY_PROFICIENCY": 96,
		},
		WantedWeights: roleEffectiveWeights("ANOMALY", "ANOMALY_AP", nil),
	}
}

func TestRemielleScreenshotPanelCalibration(t *testing.T) {
	req := remielleScreenshotRequest()
	res, ok := evaluateBuild(req.Discs, req, nil)
	if !ok {
		t.Fatal("蕾米埃尔实机六盘未通过 evaluateBuild")
	}
	if res.FinalHP != 10131 || res.FinalAttack != 4078 || res.FinalDefense != 901 {
		t.Fatalf("蕾米埃尔面板生命/攻击/防御 = %.0f/%.0f/%.0f; want 10131/4078/901", res.FinalHP, res.FinalAttack, res.FinalDefense)
	}
	if !almostEqual(res.PanelCritRate, 9.8) || !almostEqual(res.PanelCritDmg, 64.4) {
		t.Fatalf("蕾米埃尔面板暴击/暴伤 = %.1f/%.1f; want 9.8/64.4", res.PanelCritRate, res.PanelCritDmg)
	}
	if res.Stats["ANOMALY_PROFICIENCY"] != 436 || res.GameEffectiveWords != 31 {
		t.Fatalf("蕾米埃尔异常精通/有效词条 = %.0f/%.0f; want 436/31", res.Stats["ANOMALY_PROFICIENCY"], res.GameEffectiveWords)
	}
	if res.Stats["ATK_PERCENT"] != 133 || res.Stats["ATK_FLAT"] != 430 {
		t.Fatalf("蕾米埃尔汇总攻击力%%/固定攻击 = %.0f/%.0f; want 133/430", res.Stats["ATK_PERCENT"], res.Stats["ATK_FLAT"])
	}
}

func TestRemielleScreenshotBuildIsOptimizable(t *testing.T) {
	resp := optimize(context.Background(), remielleScreenshotRequest())
	if len(resp.Results) != 1 {
		t.Fatalf("蕾米埃尔实机六盘应返回 1 个方案，got %d; message=%s counts=%#v", len(resp.Results), resp.Message, resp.CandidateCounts)
	}
	if resp.Results[0].FinalAttack != 4078 || resp.Results[0].Stats["ANOMALY_PROFICIENCY"] != 436 {
		t.Fatalf("优化结果面板 = attack %.0f / AP %.0f; want 4078 / 436", resp.Results[0].FinalAttack, resp.Results[0].Stats["ANOMALY_PROFICIENCY"])
	}
}

func TestRemielleDefaultsToUploadedPanelCalibration(t *testing.T) {
	index, err := webFiles.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	marker := "name:'蕾米埃尔',fullName:'蕾米埃尔·丹',rank:'S',element:'LUMIFLUX',faction:'达识结社',role:'ANOMALY',hp:7482,atk:823,def:600,impact:83,coreDefault:'0'"
	if !bytes.Contains(index, []byte(marker)) {
		t.Fatal("蕾米埃尔必须默认使用未升级技能 / 实机截图校准口径")
	}
}

func TestDualReleaseRoutes(t *testing.T) {
	versionA, err := newAppMux(false)
	if err != nil {
		t.Fatal(err)
	}
	requestA := httptest.NewRequest(http.MethodGet, "/api/scanner/start", nil)
	responseA := httptest.NewRecorder()
	versionA.ServeHTTP(responseA, requestA)
	if responseA.Code != http.StatusNotFound {
		t.Fatalf("V1.02A scanner route status = %d; want 404", responseA.Code)
	}

	versionB, err := newAppMux(true)
	if err != nil {
		t.Fatal(err)
	}
	requestB := httptest.NewRequest(http.MethodGet, "/api/scanner/start", nil)
	responseB := httptest.NewRecorder()
	versionB.ServeHTTP(responseB, requestB)
	if responseB.Code != http.StatusMethodNotAllowed {
		t.Fatalf("V1.02B scanner route status = %d; want 405", responseB.Code)
	}
}

func TestVersion121AgentRosterAndDefenseSupport(t *testing.T) {
	index, err := webFiles.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"凯撒", "赛斯", "本", "潘引壶", "照", "希希芙", "普罗米娅", "诺姆", "蕾米埃尔"} {
		if !bytes.Contains(index, []byte("name:'"+name+"'")) {
			t.Fatalf("v1.21 agent roster is missing %s", name)
		}
	}
	if bytes.Contains(index, []byte("name:'普尔克拉'")) {
		t.Fatal("duplicate agent 普尔克拉 should be removed; only 波可娜 is retained")
	}
	if !bytes.Contains(index, []byte("name:'照',rank:'S',element:'ICE',faction:'坎卜斯黑枝',role:'DEFENSE'")) {
		t.Fatal("照 should be classified as DEFENSE")
	}
	if bytes.Count(index, []byte("name:'星见雅'")) != 1 || !bytes.Contains(index, []byte("name:'星见雅',role:'ANOMALY'")) {
		t.Fatal("星见雅 should have exactly one ANOMALY record")
	}
	if bytes.Contains(index, []byte("特殊异常→强攻")) || bytes.Contains(index, []byte("specialNote")) {
		t.Fatal("obsolete special Attack entry for 星见雅 should be removed")
	}
	weights := roleEffectiveWeights("DEFENSE", "UTILITY_BALANCE", nil)
	if weights["DEF_PERCENT"] != 1 || weights["HP_PERCENT"] <= 0 || weights["ATK_PERCENT"] <= 0 {
		t.Fatalf("defense weights are incomplete: %#v", weights)
	}
	if !roleIsUtility("DEFENSE") {
		t.Fatal("DEFENSE should use utility target-window planning")
	}
	if got := calcFinalDefense(724, 0, 48, 184); got != 1255 {
		t.Fatalf("final defense = %.0f; want 1255", got)
	}
}

func TestVersion31RemielleAndDriveDiscSupport(t *testing.T) {
	index, err := webFiles.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{
		"fullName:'蕾米埃尔·丹'", "element:'LUMIFLUX'", "faction:'达识结社'",
		"name:'空羽复归之诗'", "'谶羽之誓'", "'荆棘玫瑰'", "LUMIFLUX:'流明'",
	} {
		if !bytes.Contains(index, []byte(marker)) {
			t.Fatalf("v1.21 Remielle marker missing: %s", marker)
		}
	}
	if got, ok := ocrDefaultMainStatValue("LUMIFLUX_DMG"); !ok || got != 30 {
		t.Fatalf("LUMIFLUX_DMG default main stat = %v, %v; want 30, true", got, ok)
	}
	if slot, ok := uniqueOCRSlotForMainStat("LUMIFLUX_DMG"); !ok || slot != 5 {
		t.Fatalf("LUMIFLUX_DMG slot = %d, %v; want 5, true", slot, ok)
	}
	if got := statTypeFromOCRLabel("流明属性伤害加成 30%", true); got != "LUMIFLUX_DMG" {
		t.Fatalf("OCR stat type = %q; want LUMIFLUX_DMG", got)
	}
	if got := elementDamageStatKey("LUMIFLUX"); got != "LUMIFLUX_DMG" {
		t.Fatalf("Lumiflux element damage key = %q", got)
	}

	panel := map[string]float64{}
	applyTwoPiecePanelBonuses(panel, map[string]int{"谶羽之誓": 2, "荆棘玫瑰": 2})
	if panel["ANOMALY_PROFICIENCY"] != 30 || panel["DEF_PERCENT"] != 16 {
		t.Fatalf("3.1 two-piece bonuses = %#v", panel)
	}
	combat := map[string]float64{}
	applyFourPieceCombatBonuses(combat, map[string]int{"谶羽之誓": 4})
	applyConditionalFourPieceCombatBonuses(combat, map[string]int{"谶羽之誓": 4}, "LUMIFLUX")
	if combat["ANOMALY_PROFICIENCY"] != 50 || combat["ANOMALY_DMG_BONUS"] != 15 {
		t.Fatalf("Feathered Fate combat bonuses = %#v", combat)
	}
}

func TestInventoryCardViewportAndDetailLayout(t *testing.T) {
	index, err := webFiles.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{
		"grid-auto-rows: 192px",
		"max-height: 798px",
		"overflow-y: auto",
		"width: 42px; height: 42px",
		"inventoryDetailSubStats",
		"inventoryDetailPrimaryStat",
		"font-size: 24px",
		"<strong>副属性</strong>",
		"Array.from({length:4}",
	} {
		if !bytes.Contains(index, []byte(marker)) {
			t.Fatalf("inventory UI regression marker missing: %s", marker)
		}
	}
}

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
		t.Fatalf("Dawn 4pc should not affect panel stats in v1.18, got %#v", etherPanel)
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

func sv(t string, v float64) StatValue { return StatValue{Type: t, Value: v} }

func testDisc(set string, slot int, main StatValue, subs ...StatValue) Disc {
	return Disc{ID: newID(), SetName: set, Slot: slot, Rarity: "S", Level: 15, MainStat: main, SubStats: subs}
}

func mustEvalWords(t *testing.T, name, role string, discs []Disc, required4, required2 string) float64 {
	t.Helper()
	res, ok := evaluateBuild(discs, OptimizeRequest{
		RoleSystem:    role,
		CharacterName: name,
		SetPattern:    "4+2",
		Required4Set:  required4,
		Required2Set:  required2,
		BaseCritRate:  5,
		BaseCritDmg:   50,
		WantedWeights: roleEffectiveWeights(role, "", nil),
	}, nil)
	if !ok {
		t.Fatalf("build for %s did not evaluate", name)
	}
	return res.GameEffectiveWords
}

func yixuanScreenshotDiscs() []Disc {
	return []Disc{
		testDisc("云岿如我", 1, sv("HP_FLAT", 2200), sv("HP_PERCENT", 6), sv("CRIT_RATE", 2.4), sv("ATK_PERCENT", 3), sv("CRIT_DMG", 19.2)),
		testDisc("折枝剑歌", 2, sv("ATK_FLAT", 316), sv("CRIT_RATE", 2.4), sv("CRIT_DMG", 14.4), sv("HP_PERCENT", 9), sv("ATK_PERCENT", 3)),
		testDisc("云岿如我", 3, sv("DEF_FLAT", 184), sv("DEF_PERCENT", 9.6), sv("CRIT_RATE", 4.8), sv("HP_PERCENT", 6), sv("CRIT_DMG", 14.4)),
		testDisc("折枝剑歌", 4, sv("CRIT_RATE", 24), sv("CRIT_DMG", 14.4), sv("HP_PERCENT", 9), sv("PEN_FLAT", 18), sv("ATK_FLAT", 19)),
		testDisc("云岿如我", 5, sv("ETHER_DMG", 30), sv("ATK_FLAT", 57), sv("CRIT_DMG", 9.6), sv("CRIT_RATE", 2.4), sv("HP_FLAT", 224)),
		testDisc("云岿如我", 6, sv("HP_PERCENT", 30), sv("CRIT_DMG", 14.4), sv("ATK_PERCENT", 6), sv("ATK_FLAT", 38), sv("CRIT_RATE", 4.8)),
	}
}

func TestProPanelCalibrationMatchesYixuanScreenshot(t *testing.T) {
	res, ok := evaluateBuild(yixuanScreenshotDiscs(), OptimizeRequest{
		RoleSystem:       "RUPTURE",
		CharacterName:    "仪玄",
		CharacterElement: "ETHER",
		SetPattern:       "4+2",
		Required4Set:     "云岿如我",
		Required2Set:     "折枝剑歌",
		BaseHP:           7953,
		BaseATK:          872,
		BaseCritRate:     5,
		BaseCritDmg:      50,
		HPToSheerRatio:   0.1,
		ExtraStats: map[string]float64{
			"BASE_HP":    420,
			"BASE_ATK":   743,
			"HP_PERCENT": 30,
			"CRIT_RATE":  14.4,
		},
		CombatExtraStats: map[string]float64{"CRIT_RATE": 20},
		WantedWeights:    roleEffectiveWeights("RUPTURE", "", nil),
	}, nil)
	if !ok {
		t.Fatal("仪玄截图配装未通过 evaluateBuild")
	}
	if res.FinalHP != 19170 || res.FinalAttack != 2238 {
		t.Fatalf("仪玄面板生命/攻击 = %.0f/%.0f; want 19170/2238", res.FinalHP, res.FinalAttack)
	}
	if !almostEqual(res.PanelCritRate, 60.2) || !almostEqual(res.PanelCritDmg, 152.4) {
		t.Fatalf("仪玄面板暴击/暴伤 = %.1f/%.1f; want 60.2/152.4", res.PanelCritRate, res.PanelCritDmg)
	}
	if res.SheerForce != 2588 {
		t.Fatalf("仪玄贯穿力 = %.0f; want 2588", res.SheerForce)
	}
	if res.GameEffectiveWords != 35 {
		t.Fatalf("仪玄游戏有效词条 = %.1f; want 35", res.GameEffectiveWords)
	}
	if res.Stats["ATK_PERCENT"] != 12 {
		t.Fatalf("不计为仪玄游戏有效词条的攻击力%%仍应进入面板汇总，got %.1f; want 12", res.Stats["ATK_PERCENT"])
	}
	if res.EffectiveWords <= res.GameEffectiveWords {
		t.Fatalf("总副词条 %.1f 应大于游戏有效词条 %.1f，以证明两种口径已拆分", res.EffectiveWords, res.GameEffectiveWords)
	}
}

func TestGameEffectiveWordsMatchUploadedPanelScreenshots(t *testing.T) {
	nangong := []Disc{
		testDisc("法厄同之歌", 1, sv("HP_FLAT", 2200), sv("ATK_FLAT", 19), sv("HP_PERCENT", 6), sv("DEF_PERCENT", 4.8), sv("ANOMALY_PROFICIENCY", 36)),
		testDisc("自由蓝调", 2, sv("ATK_FLAT", 316), sv("DEF_PERCENT", 9.6), sv("PEN_FLAT", 9), sv("HP_FLAT", 224), sv("ANOMALY_PROFICIENCY", 36)),
		testDisc("法厄同之歌", 3, sv("DEF_FLAT", 184), sv("DEF_PERCENT", 9.6), sv("CRIT_DMG", 9.6), sv("ANOMALY_PROFICIENCY", 36), sv("HP_FLAT", 112)),
		testDisc("法厄同之歌", 4, sv("ANOMALY_PROFICIENCY", 92), sv("CRIT_DMG", 4.8), sv("CRIT_RATE", 4.8), sv("ATK_PERCENT", 9), sv("ATK_FLAT", 57)),
		testDisc("法厄同之歌", 5, sv("ETHER_DMG", 30), sv("ANOMALY_PROFICIENCY", 18), sv("ATK_PERCENT", 12), sv("DEF_FLAT", 15), sv("ATK_FLAT", 19)),
		testDisc("自由蓝调", 6, sv("ANOMALY_MASTERY", 30), sv("ANOMALY_PROFICIENCY", 18), sv("ATK_PERCENT", 12), sv("DEF_FLAT", 15), sv("PEN_FLAT", 9)),
	}
	if got := mustEvalWords(t, "南宫羽", "STUN", nangong, "法厄同之歌", "自由蓝调"); got != 27 {
		t.Fatalf("南宫羽有效词条 = %.1f; want 27", got)
	}

	aria := []Disc{
		testDisc("荧光蛛眼", 1, sv("HP_FLAT", 2200), sv("ANOMALY_PROFICIENCY", 36), sv("CRIT_DMG", 9.6), sv("DEF_PERCENT", 4.8), sv("DEF_FLAT", 15)),
		testDisc("法厄同之歌", 2, sv("ATK_FLAT", 316), sv("ATK_PERCENT", 6), sv("ANOMALY_PROFICIENCY", 36), sv("CRIT_DMG", 4.8), sv("PEN_FLAT", 18)),
		testDisc("荧光蛛眼", 3, sv("DEF_FLAT", 184), sv("HP_FLAT", 224), sv("ATK_PERCENT", 6), sv("ATK_FLAT", 19), sv("ANOMALY_PROFICIENCY", 36)),
		testDisc("荧光蛛眼", 4, sv("ANOMALY_PROFICIENCY", 92), sv("HP_FLAT", 112), sv("ATK_FLAT", 19), sv("CRIT_DMG", 19.2), sv("ATK_PERCENT", 9)),
		testDisc("荧光蛛眼", 5, sv("ATK_PERCENT", 30), sv("ANOMALY_PROFICIENCY", 27), sv("HP_PERCENT", 6), sv("CRIT_DMG", 4.8), sv("DEF_FLAT", 15)),
		testDisc("法厄同之歌", 6, sv("ANOMALY_MASTERY", 30), sv("CRIT_DMG", 14.4), sv("DEF_FLAT", 15), sv("ANOMALY_PROFICIENCY", 27), sv("PEN_FLAT", 18)),
	}
	if got := mustEvalWords(t, "爱芮", "ANOMALY", aria, "荧光蛛眼", "法厄同之歌"); got != 25 {
		t.Fatalf("爱芮有效词条 = %.1f; want 25", got)
	}

	velina := []Disc{
		testDisc("月光骑士颂", 1, sv("HP_FLAT", 2200), sv("DEF_PERCENT", 14.4), sv("ATK_PERCENT", 6), sv("CRIT_DMG", 9.6), sv("ANOMALY_PROFICIENCY", 18)),
		testDisc("呼啸沙龙", 2, sv("ATK_FLAT", 316), sv("ANOMALY_PROFICIENCY", 36), sv("DEF_PERCENT", 4.8), sv("HP_FLAT", 112), sv("ATK_PERCENT", 6)),
		testDisc("呼啸沙龙", 3, sv("DEF_FLAT", 184), sv("ATK_FLAT", 19), sv("ANOMALY_PROFICIENCY", 18), sv("HP_PERCENT", 3), sv("ATK_PERCENT", 12)),
		testDisc("月光骑士颂", 4, sv("ANOMALY_PROFICIENCY", 92), sv("ATK_PERCENT", 9), sv("DEF_FLAT", 45), sv("PEN_FLAT", 9), sv("ATK_FLAT", 38)),
		testDisc("呼啸沙龙", 5, sv("ATK_PERCENT", 30), sv("ANOMALY_PROFICIENCY", 18), sv("ATK_PERCENT", 6), sv("ATK_FLAT", 57), sv("HP_FLAT", 112)),
		testDisc("呼啸沙龙", 6, sv("ENERGY_REGEN", 60), sv("ANOMALY_PROFICIENCY", 27), sv("CRIT_DMG", 4.8), sv("DEF_PERCENT", 9.6), sv("ATK_PERCENT", 3)),
	}
	if got := mustEvalWords(t, "维琳娜", "ANOMALY", velina, "呼啸沙龙", "月光骑士颂"); got != 27 {
		t.Fatalf("维琳娜有效词条 = %.1f; want 27", got)
	}

	yixuan := yixuanScreenshotDiscs()
	if got := mustEvalWords(t, "仪玄", "RUPTURE", yixuan, "云岿如我", "折枝剑歌"); got != 35 {
		t.Fatalf("仪玄有效词条 = %.1f; want 35", got)
	}
}
