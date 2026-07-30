package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf16"
)

//go:embed web/index.html web/assets
var webFiles embed.FS

const appVersion = 121

// releaseEdition is set to A or B at build time with -ldflags "-X main.releaseEdition=A".
// appVersion remains the persisted-state schema version so both editions can open
// the same inventory without migrations.
const releaseSeries = "1.03"

var releaseEdition = "B"

func normalizedReleaseEdition(edition string) string {
	if strings.EqualFold(strings.TrimSpace(edition), "A") {
		return "A"
	}
	return "B"
}

func releaseLabelForEdition(edition string) string {
	return "v" + releaseSeries + normalizedReleaseEdition(edition)
}

func releaseLabel() string {
	return releaseLabelForEdition(releaseEdition)
}

func scannerIncludedForEdition(edition string) bool {
	return normalizedReleaseEdition(edition) == "B"
}

func scannerIncluded() bool {
	return scannerIncludedForEdition(releaseEdition)
}

// critDisplayTolerance is used only for human-facing wording.
const critDisplayTolerance = 0.30

// critTargetTolerance is one S-rank CRIT Rate sub-stat roll. For crit-oriented
// builds the user-entered panel goal is treated as a target window rather than a
// one-sided hard floor: panel CRIT Rate may be at most one roll below or one roll
// above the target. Values outside that window are filtered out.
const critTargetTolerance = 2.40

// anomalyTargetTolerance allows anomaly builds to be slightly below the requested
// Anomaly Proficiency target. Unlike Crit Rate, AP has no overcap waste, so values
// above the target remain fully valuable.
const anomalyTargetTolerance = 10.0

// utilityTargetRelativeTolerance is used for Support/Stun target windows for HP/ATK.
// These attributes are treated as planning targets: a small shortfall is acceptable,
// and excessive over-target values are down-ranked as wasted.
const utilityTargetRelativeTolerance = 0.025

type StatValue struct {
	Type  string  `json:"type"`
	Value float64 `json:"value"`
	// Raw is used only by OCR preview rows, so users can locate an
	// unrecognized sub-stat without shifting following rows upward.
	Raw string `json:"raw,omitempty"`
	// Suspect is used only by OCR import preview. It marks fields that were
	// inferred from fuzzy text or value patterns and should be checked.
	Suspect bool `json:"suspect,omitempty"`
	// Extra preserves zzz_calculator interoperability fields such as stat,
	// mode, label and rawValue without exposing them in this app's editor.
	Extra map[string]json.RawMessage `json:"-"`
}

type Disc struct {
	ID      string `json:"id"`
	SetName string `json:"setName"`
	Slot    int    `json:"slot"`
	Rarity  string `json:"rarity"`
	Level   int    `json:"level"`
	// Stats is kept for compatibility with older imports.
	// New data uses one MainStat plus up to four SubStats.
	Stats      []StatValue `json:"stats,omitempty"`
	MainStat   StatValue   `json:"mainStat"`
	SubStats   []StatValue `json:"subStats"`
	Locked     bool        `json:"locked"`
	Discarded  bool        `json:"discarded"`
	EquippedBy string      `json:"equippedBy"`
	Note       string      `json:"note"`
	CreatedAt  string      `json:"createdAt"`
	UpdatedAt  string      `json:"updatedAt"`
	// Extra preserves calculator/scanner fields that this optimizer does not
	// use (setId, maxLevel, source, raw, reservations, future extensions, ...).
	// They are emitted at their original JSON level so a round trip is lossless.
	Extra map[string]json.RawMessage `json:"-"`
}

func unmarshalWithExtras(data []byte, target any, known ...string) (map[string]json.RawMessage, error) {
	if err := json.Unmarshal(data, target); err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	for _, key := range known {
		delete(fields, key)
	}
	if len(fields) == 0 {
		return nil, nil
	}
	return fields, nil
}

func marshalWithExtras(base any, extras map[string]json.RawMessage) ([]byte, error) {
	data, err := json.Marshal(base)
	if err != nil {
		return nil, err
	}
	if len(extras) == 0 {
		return data, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	for key, value := range extras {
		if _, known := fields[key]; known || len(value) == 0 {
			continue
		}
		fields[key] = value
	}
	return json.Marshal(fields)
}

func (stat *StatValue) UnmarshalJSON(data []byte) error {
	type statAlias StatValue
	var decoded statAlias
	extras, err := unmarshalWithExtras(data, &decoded, "type", "value", "raw", "suspect")
	if err != nil {
		return err
	}
	*stat = StatValue(decoded)
	stat.Extra = extras
	return nil
}

func (stat StatValue) MarshalJSON() ([]byte, error) {
	type statAlias StatValue
	return marshalWithExtras(statAlias(stat), stat.Extra)
}

func (disc *Disc) UnmarshalJSON(data []byte) error {
	type discAlias Disc
	var decoded discAlias
	extras, err := unmarshalWithExtras(data, &decoded,
		"id", "setName", "slot", "rarity", "level", "stats", "mainStat", "subStats",
		"locked", "discarded", "equippedBy", "note", "createdAt", "updatedAt")
	if err != nil {
		return err
	}
	*disc = Disc(decoded)
	disc.Extra = extras
	return nil
}

func (disc Disc) MarshalJSON() ([]byte, error) {
	type discAlias Disc
	return marshalWithExtras(discAlias(disc), disc.Extra)
}

type SetEffect struct {
	SetName   string  `json:"setName"`
	TwoStat   string  `json:"twoStat"`
	TwoValue  float64 `json:"twoValue"`
	FourStat  string  `json:"fourStat"`
	FourValue float64 `json:"fourValue"`
	Note      string  `json:"note"`
}

type CharacterBuild struct {
	ID        string   `json:"id,omitempty"`
	Character string   `json:"character"`
	Name      string   `json:"name,omitempty"`
	DiscIDs   []string `json:"discIds"`
	CreatedAt string   `json:"createdAt"`
	UpdatedAt string   `json:"updatedAt"`
}

type DiscClaim struct {
	DiscID    string `json:"discId"`
	BuildID   string `json:"buildId,omitempty"`
	BuildName string `json:"buildName,omitempty"`
	Character string `json:"character,omitempty"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

type AppState struct {
	Version           int              `json:"version"`
	Discs             []Disc           `json:"discs"`
	SetEffects        []SetEffect      `json:"setEffects"`
	CharacterBuilds   []CharacterBuild `json:"characterBuilds"`
	DiscClaims        []DiscClaim      `json:"discClaims"`
	ClaimsInitialized bool             `json:"claimsInitialized"`
}

type StateResponse struct {
	Version                 int              `json:"version"`
	Discs                   []Disc           `json:"discs"`
	SetEffects              []SetEffect      `json:"setEffects"`
	CharacterBuilds         []CharacterBuild `json:"characterBuilds"`
	DiscClaims              []DiscClaim      `json:"discClaims"`
	ClaimsInitialized       bool             `json:"claimsInitialized"`
	StoragePath             string           `json:"storagePath"`
	DefaultStoragePath      string           `json:"defaultStoragePath"`
	UsingDefaultStoragePath bool             `json:"usingDefaultStoragePath"`
}

type StoragePathRequest struct {
	Path  string `json:"path"`
	Reset bool   `json:"reset"`
}

type storageConfig struct {
	StoragePath string `json:"storagePath"`
}

type OptimizeRequest struct {
	Discs                    []Disc              `json:"discs"`
	SetEffects               []SetEffect         `json:"setEffects"`
	BaseCritRate             float64             `json:"baseCritRate"`
	BaseCritDmg              float64             `json:"baseCritDmg"`
	BaseHP                   float64             `json:"baseHP"`
	BaseATK                  float64             `json:"baseATK"`
	BaseDEF                  float64             `json:"baseDEF"`
	BaseAnomalyMastery       float64             `json:"baseAnomalyMastery"`
	BaseEnergyRegen          float64             `json:"baseEnergyRegen"`
	HPToSheerRatio           float64             `json:"hpToSheerRatio"`
	RoleSystem               string              `json:"roleSystem"`
	CharacterName            string              `json:"characterName"`
	CharacterElement         string              `json:"characterElement"`
	ExtraCritRate            float64             `json:"extraCritRate"`
	ExtraCritDmg             float64             `json:"extraCritDmg"`
	TargetCritRate           float64             `json:"targetCritRate"`
	TargetCritDmg            float64             `json:"targetCritDmg"`
	TargetAnomalyProficiency float64             `json:"targetAnomalyProficiency"`
	TargetFinalHP            float64             `json:"targetFinalHp"`
	TargetFinalDefense       float64             `json:"targetFinalDefense"`
	TargetFinalAttack        float64             `json:"targetFinalAttack"`
	Mode                     string              `json:"mode"`
	WantedWeights            map[string]float64  `json:"wantedWeights"`
	TopN                     int                 `json:"topN"`
	TopKPerSlot              int                 `json:"topKPerSlot"`
	MaxCombinations          int64               `json:"maxCombinations"`
	ExcludeDiscarded         bool                `json:"excludeDiscarded"`
	SlotAllowedMainStats     map[string][]string `json:"slotAllowedMainStats"`
	SetPattern               string              `json:"setPattern"`
	Required4Set             string              `json:"required4Set"`
	Required2Set             string              `json:"required2Set"`
	WordCoef                 float64             `json:"wordCoef"`
	OverflowPenalty          float64             `json:"overflowPenalty"`
	ExtraStats               map[string]float64  `json:"extraStats"`
	CombatExtraStats         map[string]float64  `json:"combatExtraStats"`
	MinPanelCritDmg          float64             `json:"minPanelCritDmg"`
	MinFinalAttack           float64             `json:"minFinalAttack"`
	MinSheerForce            float64             `json:"minSheerForce"`
}

type OptimizeResult struct {
	Rank                int                `json:"rank"`
	Score               float64            `json:"score"`
	OutputScore         float64            `json:"outputScore"`
	CritRate            float64            `json:"critRate"`
	CritDmg             float64            `json:"critDmg"`
	PanelCritRate       float64            `json:"panelCritRate"`
	PanelCritDmg        float64            `json:"panelCritDmg"`
	CritOverflow        float64            `json:"critOverflow"`
	CritShortfall       float64            `json:"critShortfall"`
	CritWaste           float64            `json:"critWaste"`
	CritFitPenalty      float64            `json:"critFitPenalty"`
	CritFitFactor       float64            `json:"critFitFactor"`
	EffectiveWords      float64            `json:"effectiveWords"`
	WeightedWords       float64            `json:"weightedWords"`
	GameEffectiveWords  float64            `json:"gameEffectiveWords"`
	FinalAttack         float64            `json:"finalAttack"`
	FinalHP             float64            `json:"finalHp"`
	FinalDefense        float64            `json:"finalDefense"`
	CombatFinalAttack   float64            `json:"combatFinalAttack"`
	CombatFinalHP       float64            `json:"combatFinalHp"`
	CombatFinalDefense  float64            `json:"combatFinalDefense"`
	InitialEnergyRegen  float64            `json:"initialEnergyRegen"`
	FinalAnomalyMastery float64            `json:"finalAnomalyMastery"`
	SheerForce          float64            `json:"sheerForce"`
	CritMultiplier      float64            `json:"critMultiplier"`
	DamageIndex         float64            `json:"damageIndex"`
	Stats               map[string]float64 `json:"stats"`
	CombatStats         map[string]float64 `json:"combatStats"`
	SetSummary          map[string]int     `json:"setSummary"`
	Discs               []Disc             `json:"discs"`
	Reason              string             `json:"reason"`
}

type OptimizeResponse struct {
	Results              []OptimizeResult `json:"results"`
	NearMisses           []OptimizeResult `json:"nearMisses,omitempty"`
	TotalDiscs           int              `json:"totalDiscs"`
	CandidateCounts      map[string]int   `json:"candidateCounts"`
	SearchedCombinations int64            `json:"searchedCombinations"`
	SkippedReason        string           `json:"skippedReason"`
	Message              string           `json:"message"`
	Canceled             bool             `json:"canceled,omitempty"`
}

// OCRResponse is returned by the experimental image-recognition import.
// It intentionally keeps the raw text and warnings so the user can review
// and correct the generated data before saving it into the inventory.
type OCRParseRequest struct {
	RawText string `json:"rawText"`
	Engine  string `json:"engine"`
}

type OCRResponse struct {
	Engine         string      `json:"engine"`
	RawText        string      `json:"rawText"`
	SetName        string      `json:"setName"`
	Slot           int         `json:"slot"`
	Rarity         string      `json:"rarity"`
	Level          int         `json:"level"`
	MainStat       StatValue   `json:"mainStat"`
	SubStats       []StatValue `json:"subStats"`
	Warnings       []string    `json:"warnings"`
	DoubtfulFields []string    `json:"doubtfulFields,omitempty"`
}

type serverState struct {
	mu          sync.RWMutex
	state       AppState
	storagePath string
}

var srvState serverState

type scannerBundleManifest struct {
	Version    string            `json:"version"`
	ReleaseTag string            `json:"releaseTag"`
	Source     string            `json:"source"`
	Package    string            `json:"package"`
	Files      map[string]string `json:"files"`
}

var scannerRuntime struct {
	sync.Mutex
	cmd *exec.Cmd
}

var optimizerState struct {
	mu      sync.Mutex
	cancel  context.CancelFunc
	started time.Time
	id      int64
}

var rollValue = map[string]float64{
	"CRIT_RATE":           2.4,
	"CRIT_DMG":            4.8,
	"ATK_PERCENT":         3.0,
	"HP_PERCENT":          3.0,
	"DEF_PERCENT":         4.8,
	"ATK_FLAT":            19.0,
	"HP_FLAT":             112.0,
	"DEF_FLAT":            15.0,
	"PEN_FLAT":            9.0,
	"ANOMALY_PROFICIENCY": 9.0,
}

var builtinSetNames = []string{
	"啄木鸟电音", "震星迪斯科", "河豚电音", "激素朋克", "摇摆爵士", "灵魂摇滚", "自由蓝调", "极地重金属", "炎狱重金属", "雷暴重金属", "獠牙重金属", "混沌重金属", "原始朋克", "混沌爵士", "折枝剑歌", "静听嘉音", "法厄同之歌", "如影相随", "云岿如我", "山大王", "月光骑士颂", "拂晓生花", "雪兔梦游仙境", "囚徒手记", "流光咏叹", "沧浪行歌", "呼啸沙龙", "拂晓行纪", "荧光蛛眼", "谶羽之誓", "荆棘玫瑰",
}

var builtinSetNameLookup = func() map[string]string {
	m := map[string]string{}
	for _, name := range builtinSetNames {
		m[name] = name
		m[compactHan(name)] = name
	}
	return m
}()

var ocrStatLabels = []string{
	"暴击伤害", "暴击率", "异常精通", "异常掌控", "能量自动回复", "能量回复", "穿透率", "穿透值", "火属性伤害", "冰属性伤害", "电属性伤害", "物理属性伤害", "以太属性伤害", "风属性伤害", "流明属性伤害", "生命值", "攻击力", "防御力", "冲击力",
}

var ocrSlotMainAllowed = map[int]map[string]bool{
	1: map[string]bool{"HP_FLAT": true},
	2: map[string]bool{"ATK_FLAT": true},
	3: map[string]bool{"DEF_FLAT": true},
	4: map[string]bool{"HP_PERCENT": true, "ATK_PERCENT": true, "DEF_PERCENT": true, "CRIT_RATE": true, "CRIT_DMG": true, "ANOMALY_PROFICIENCY": true},
	5: map[string]bool{"HP_PERCENT": true, "ATK_PERCENT": true, "DEF_PERCENT": true, "PEN_RATIO": true, "FIRE_DMG": true, "ICE_DMG": true, "ELECTRIC_DMG": true, "PHYSICAL_DMG": true, "ETHER_DMG": true, "WIND_DMG": true, "LUMIFLUX_DMG": true},
	6: map[string]bool{"HP_PERCENT": true, "ATK_PERCENT": true, "DEF_PERCENT": true, "ANOMALY_MASTERY": true, "ENERGY_REGEN": true, "IMPACT": true},
}

var defaultEffects = []SetEffect{
	{SetName: "啄木鸟电音", TwoStat: "CRIT_RATE", TwoValue: 8, FourStat: "", FourValue: 0, Note: "2件套静态加成；4件套可按需手动填写"},
	{SetName: "激素朋克", TwoStat: "ATK_PERCENT", TwoValue: 10, FourStat: "", FourValue: 0, Note: "2件套静态加成"},
	{SetName: "河豚电音", TwoStat: "PEN_RATIO", TwoValue: 8, FourStat: "", FourValue: 0, Note: "2件套静态加成"},
	{SetName: "摇摆爵士", TwoStat: "ENERGY_REGEN", TwoValue: 20, FourStat: "", FourValue: 0, Note: "2件套静态加成"},
	{SetName: "自由蓝调", TwoStat: "ANOMALY_PROFICIENCY", TwoValue: 30, FourStat: "", FourValue: 0, Note: "2件套静态加成"},
	{SetName: "混沌爵士", TwoStat: "ANOMALY_PROFICIENCY", TwoValue: 30, FourStat: "", FourValue: 0, Note: "2件套静态加成"},
	{SetName: "炎狱重金属", TwoStat: "FIRE_DMG", TwoValue: 10, FourStat: "", FourValue: 0, Note: "2件套静态加成"},
	{SetName: "极地重金属", TwoStat: "ICE_DMG", TwoValue: 10, FourStat: "", FourValue: 0, Note: "2件套静态加成"},
	{SetName: "雷暴重金属", TwoStat: "ELECTRIC_DMG", TwoValue: 10, FourStat: "", FourValue: 0, Note: "2件套静态加成"},
	{SetName: "呼啸沙龙", TwoStat: "WIND_DMG", TwoValue: 10, FourStat: "", FourValue: 0, Note: "3.0：2件套风属性伤害+10%；4件套按满触发作为实战参考"},
	{SetName: "拂晓行纪", TwoStat: "ETHER_DMG", TwoValue: 10, FourStat: "", FourValue: 0, Note: "3.0：2件套以太伤害+10%；4件套按角色属性/触发状态计算"},
	{SetName: "獠牙重金属", TwoStat: "PHYSICAL_DMG", TwoValue: 10, FourStat: "", FourValue: 0, Note: "2件套静态加成"},
	{SetName: "谶羽之誓", TwoStat: "ANOMALY_PROFICIENCY", TwoValue: 30, FourStat: "", FourValue: 0, Note: "3.1：2件套异常精通+30；4件套作为实战参考"},
	{SetName: "荆棘玫瑰", TwoStat: "DEF_PERCENT", TwoValue: 16, FourStat: "", FourValue: 0, Note: "3.1：2件套防御力+16%；4件套按初始防御阈值触发"},
}

// twoPiecePanelBonuses contains static 2-piece drive-disc effects that are visible
// or directly comparable in the agent details panel. Conditional 4-piece effects
// are intentionally not added here because the character details page does not
// show them unless a combat state is active, and their uptime differs by team and
// rotation.
var twoPiecePanelBonuses = map[string][]StatValue{
	"啄木鸟电音":  {{Type: "CRIT_RATE", Value: 8}},
	"震星迪斯科":  {{Type: "IMPACT", Value: 6}},
	"河豚电音":   {{Type: "PEN_RATIO", Value: 8}},
	"激素朋克":   {{Type: "ATK_PERCENT", Value: 10}},
	"摇摆爵士":   {{Type: "ENERGY_REGEN", Value: 20}},
	"灵魂摇滚":   {{Type: "DEF_PERCENT", Value: 16}},
	"自由蓝调":   {{Type: "ANOMALY_PROFICIENCY", Value: 30}},
	"极地重金属":  {{Type: "ICE_DMG", Value: 10}},
	"炎狱重金属":  {{Type: "FIRE_DMG", Value: 10}},
	"雷暴重金属":  {{Type: "ELECTRIC_DMG", Value: 10}},
	"獠牙重金属":  {{Type: "PHYSICAL_DMG", Value: 10}},
	"混沌重金属":  {{Type: "ETHER_DMG", Value: 10}},
	"混沌爵士":   {{Type: "ANOMALY_PROFICIENCY", Value: 30}},
	"折枝剑歌":   {{Type: "CRIT_DMG", Value: 16}},
	"静听嘉音":   {{Type: "ATK_PERCENT", Value: 10}},
	"法厄同之歌":  {{Type: "ANOMALY_MASTERY", Value: 8}},
	"云岿如我":   {{Type: "HP_PERCENT", Value: 10}},
	"月光骑士颂":  {{Type: "ENERGY_REGEN", Value: 20}},
	"沧浪行歌":   {{Type: "PHYSICAL_DMG", Value: 10}},
	"流光咏叹":   {{Type: "ETHER_DMG", Value: 10}},
	"雪兔梦游仙境": {{Type: "HP_PERCENT", Value: 10}},
	"囚徒手记":   {{Type: "ICE_DMG", Value: 10}},
	"呼啸沙龙":   {{Type: "WIND_DMG", Value: 10}},
	"拂晓行纪":   {{Type: "ETHER_DMG", Value: 10}},
	"谶羽之誓":   {{Type: "ANOMALY_PROFICIENCY", Value: 30}},
	"荆棘玫瑰":   {{Type: "DEF_PERCENT", Value: 16}},
}

// fourPieceCombatBonuses contains stable, commonly-assumed in-combat bonuses used
// only for ranking reference. They are intentionally not mixed into panel stats
// or the user-entered panel crit-rate target.
var fourPieceCombatBonuses = map[string][]StatValue{
	// 云岿如我：强化特殊技/连携技/终结技触发后暴击率+4%，最多3层；满层贯穿伤害+10%。
	"云岿如我": {{Type: "CRIT_RATE", Value: 12}, {Type: "SHEER_DMG_BONUS", Value: 10}},
	// 呼啸沙龙：强化特殊技叠满后异常精通+50；触发风化后造成伤害+18%。
	"呼啸沙龙": {{Type: "ANOMALY_PROFICIENCY", Value: 50}, {Type: "ELEMENT_DMG", Value: 18}},
	// 拂晓行纪：强化特殊技/终结技触发后的攻击力+10%按实战参考处理；
	// 以太角色的4件套暴伤+30%由条件战斗加成函数按角色属性处理。
	"拂晓行纪": {{Type: "ATK_PERCENT", Value: 10}},
	// 谶羽之誓：前场触发后或位于后台时异常精通+50；流明异常伤害由条件函数单独展示。
	"谶羽之誓": {{Type: "ANOMALY_PROFICIENCY", Value: 50}},
}

func main() {
	storagePath, err := appStoragePath()
	if err != nil {
		log.Fatalf("无法确定数据保存路径: %v", err)
	}
	state, err := loadState(storagePath)
	if err != nil {
		log.Printf("读取数据失败，将使用空库存: %v", err)
		state = defaultState()
	}
	srvState = serverState{state: state, storagePath: storagePath}

	mux, err := newAppMux(scannerIncluded())
	if err != nil {
		log.Fatalf("无法加载内置网页资源: %v", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("无法启动本地服务: %v", err)
	}
	addr := listener.Addr().String()
	url := "http://" + addr + "/"

	fmt.Printf("ZZZ Drive Optimizer %s 已启动\n", releaseLabel())
	fmt.Println("数据文件:", storagePath)
	fmt.Println("浏览器地址:", url)
	fmt.Println("关闭此窗口即可退出程序。")

	go func() {
		time.Sleep(350 * time.Millisecond)
		if err := openBrowser(url); err != nil {
			log.Printf("自动打开浏览器失败，请手动访问 %s：%v", url, err)
		}
	}()

	server := &http.Server{
		Handler:           logRequests(mux),
		ReadHeaderTimeout: 10 * time.Second,
	}
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("服务异常退出: %v", err)
	}
}

func newAppMux(includeScanner bool) (*http.ServeMux, error) {
	mux := http.NewServeMux()
	webRoot, err := fs.Sub(webFiles, "web")
	if err != nil {
		return nil, err
	}
	mux.Handle("/assets/", http.FileServer(http.FS(webRoot)))
	mux.HandleFunc("/", handleIndex)
	mux.HandleFunc("/api/state", handleState)
	mux.HandleFunc("/api/storage-path", handleStoragePath)
	mux.HandleFunc("/api/storage-folder", handleStorageFolder)
	mux.HandleFunc("/api/optimize", handleOptimize)
	mux.HandleFunc("/api/optimize/cancel", handleCancelOptimize)
	mux.HandleFunc("/api/ocr", handleOCR)
	mux.HandleFunc("/api/ocr/parse", handleOCRParse)
	if includeScanner {
		mux.HandleFunc("/api/scanner/start", handleScannerStart)
	}
	mux.HandleFunc("/api/shutdown", handleShutdown)
	return mux, nil
}

func appConfigDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil || strings.TrimSpace(base) == "" {
		base, err = os.Getwd()
		if err != nil {
			return "", err
		}
	}
	dir := filepath.Join(base, "ZZZDriveBuilder")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	return dir, nil
}

func defaultStoragePath() (string, error) {
	dir, err := appConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "state.json"), nil
}

func storageConfigPath() (string, error) {
	dir, err := appConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "storage-config.json"), nil
}

func normalizeUserStoragePath(path string) (string, error) {
	path = strings.Trim(strings.TrimSpace(path), "\"'")
	if path == "" {
		return "", errors.New("路径不能为空")
	}
	if strings.HasPrefix(path, "~"+string(os.PathSeparator)) || path == "~" {
		home, err := os.UserHomeDir()
		if err == nil && strings.TrimSpace(home) != "" {
			if path == "~" {
				path = home
			} else {
				path = filepath.Join(home, strings.TrimPrefix(path, "~"+string(os.PathSeparator)))
			}
		}
	}
	path = os.ExpandEnv(path)
	if ext := strings.ToLower(filepath.Ext(path)); ext == "" {
		path = filepath.Join(path, "state.json")
	} else if ext != ".json" {
		return "", fmt.Errorf("库存文件必须是 .json 文件：%s", path)
	}
	if !filepath.IsAbs(path) {
		abs, err := filepath.Abs(path)
		if err == nil {
			path = abs
		}
	}
	return filepath.Clean(path), nil
}

func ensureStoragePath(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("路径不能为空")
	}
	dir := filepath.Dir(path)
	if dir == "." || strings.TrimSpace(dir) == "" {
		return nil
	}
	return os.MkdirAll(dir, 0755)
}

func sameStoragePath(a, b string) bool {
	a = filepath.Clean(strings.TrimSpace(a))
	b = filepath.Clean(strings.TrimSpace(b))
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func appStoragePath() (string, error) {
	def, err := defaultStoragePath()
	if err != nil {
		return "", err
	}
	if err := ensureStoragePath(def); err != nil {
		return "", err
	}
	cfgPath, err := storageConfigPath()
	if err == nil {
		if b, readErr := os.ReadFile(cfgPath); readErr == nil && len(bytes.TrimSpace(b)) > 0 {
			var cfg storageConfig
			if json.Unmarshal(b, &cfg) == nil && strings.TrimSpace(cfg.StoragePath) != "" {
				custom, normErr := normalizeUserStoragePath(cfg.StoragePath)
				if normErr == nil && ensureStoragePath(custom) == nil {
					return custom, nil
				}
				log.Printf("自定义存储路径不可用，已临时恢复默认路径: %v", normErr)
			}
		}
	}
	return def, nil
}

func saveStorageConfig(path string) error {
	cfgPath, err := storageConfigPath()
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(storageConfig{StoragePath: path}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(cfgPath, b, 0644)
}

func clearStorageConfig() error {
	cfgPath, err := storageConfigPath()
	if err != nil {
		return err
	}
	if err := os.Remove(cfgPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func defaultState() AppState {
	// v2 no longer calculates drive-disc set effects. The SetEffects field remains
	// only so older exported JSON files can still be imported without data loss.
	return AppState{Version: appVersion, Discs: []Disc{}, SetEffects: []SetEffect{}, CharacterBuilds: []CharacterBuild{}, DiscClaims: []DiscClaim{}, ClaimsInitialized: true}
}

func loadState(path string) (AppState, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return defaultState(), nil
		}
		return AppState{}, err
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return defaultState(), nil
	}
	var state AppState
	if err := json.Unmarshal(b, &state); err != nil {
		return AppState{}, err
	}
	if state.Version == 0 {
		state.Version = appVersion
	}
	if state.Discs == nil {
		state.Discs = []Disc{}
	}
	if state.SetEffects == nil {
		state.SetEffects = []SetEffect{}
	}
	if state.CharacterBuilds == nil {
		state.CharacterBuilds = []CharacterBuild{}
	}
	if state.DiscClaims == nil {
		state.DiscClaims = []DiscClaim{}
	}
	return state, nil
}

func saveState(path string, state AppState) error {
	state.Version = appVersion
	if state.Discs == nil {
		state.Discs = []Disc{}
	}
	if state.SetEffects == nil {
		state.SetEffects = []SetEffect{}
	}
	if state.CharacterBuilds == nil {
		state.CharacterBuilds = []CharacterBuild{}
	}
	if state.DiscClaims == nil {
		state.DiscClaims = []DiscClaim{}
	}
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	page, err := webFiles.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, "无法读取内置页面", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(renderIndexPage(page, releaseEdition))
}

func renderIndexPage(page []byte, edition string) []byte {
	label := releaseLabelForEdition(edition)
	scannerButton := ""
	if scannerIncludedForEdition(edition) {
		scannerButton = `<div class="scannerLaunchRow"><button id="startScannerBtn" class="scannerLaunchButton" type="button" title="打开后先点检测窗口，再点开始扫描">打开驱动盘扫描器</button></div>`
	}
	replacer := strings.NewReplacer(
		"__APP_RELEASE__", label,
		"<!--__SCANNER_BUTTON__-->", scannerButton,
		"__SCANNER_AVAILABLE__", strconv.FormatBool(scannerIncludedForEdition(edition)),
	)
	return []byte(replacer.Replace(string(page)))
}

func handleState(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		srvState.mu.RLock()
		def, _ := defaultStoragePath()
		resp := StateResponse{
			Version:                 srvState.state.Version,
			Discs:                   append([]Disc{}, srvState.state.Discs...),
			SetEffects:              append([]SetEffect{}, srvState.state.SetEffects...),
			CharacterBuilds:         append([]CharacterBuild{}, srvState.state.CharacterBuilds...),
			DiscClaims:              append([]DiscClaim{}, srvState.state.DiscClaims...),
			ClaimsInitialized:       srvState.state.ClaimsInitialized,
			StoragePath:             srvState.storagePath,
			DefaultStoragePath:      def,
			UsingDefaultStoragePath: sameStoragePath(srvState.storagePath, def),
		}
		srvState.mu.RUnlock()
		writeJSON(w, resp)
	case http.MethodPost:
		var incoming AppState
		if err := json.NewDecoder(r.Body).Decode(&incoming); err != nil {
			writeError(w, http.StatusBadRequest, "JSON 格式错误: "+err.Error())
			return
		}
		incoming.Version = appVersion
		if incoming.Discs == nil {
			incoming.Discs = []Disc{}
		}
		if incoming.SetEffects == nil {
			incoming.SetEffects = []SetEffect{}
		}
		if incoming.CharacterBuilds == nil {
			incoming.CharacterBuilds = []CharacterBuild{}
		}
		if incoming.DiscClaims == nil {
			incoming.DiscClaims = []DiscClaim{}
		}
		stamp := time.Now().Format(time.RFC3339)
		seen := map[string]bool{}
		for i := range incoming.Discs {
			if strings.TrimSpace(incoming.Discs[i].ID) == "" || seen[incoming.Discs[i].ID] {
				incoming.Discs[i].ID = newID()
			}
			seen[incoming.Discs[i].ID] = true
			if strings.TrimSpace(incoming.Discs[i].CreatedAt) == "" {
				incoming.Discs[i].CreatedAt = stamp
			}
			incoming.Discs[i].UpdatedAt = stamp
		}
		srvState.mu.Lock()
		if err := saveState(srvState.storagePath, incoming); err != nil {
			srvState.mu.Unlock()
			writeError(w, http.StatusInternalServerError, "保存失败: "+err.Error())
			return
		}
		srvState.state = incoming
		srvState.mu.Unlock()
		writeJSON(w, map[string]string{"ok": "true"})
	default:
		w.Header().Set("Allow", "GET, POST")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func scannerBundleCandidates() []string {
	candidates := []string{}
	if configured := strings.TrimSpace(os.Getenv("ZZZ_SCANNER_BUNDLE_ROOT")); configured != "" {
		candidates = append(candidates, filepath.Clean(configured))
	}
	if executable, err := os.Executable(); err == nil && strings.TrimSpace(executable) != "" {
		candidates = append(candidates, filepath.Join(filepath.Dir(executable), "scanner"))
	}
	if workingDirectory, err := os.Getwd(); err == nil && strings.TrimSpace(workingDirectory) != "" {
		for _, candidate := range []string{
			filepath.Join(workingDirectory, "scanner"),
			filepath.Join(workingDirectory, "..", "scanner"),
		} {
			duplicate := false
			for _, existing := range candidates {
				if sameStoragePath(candidate, existing) {
					duplicate = true
					break
				}
			}
			if !duplicate {
				candidates = append(candidates, filepath.Clean(candidate))
			}
		}
	}
	return candidates
}

func findScannerBundle() (string, error) {
	for _, candidate := range scannerBundleCandidates() {
		if info, err := os.Stat(filepath.Join(candidate, "ZZZ-Scanner.Next.exe")); err == nil && !info.IsDir() {
			return filepath.Clean(candidate), nil
		}
	}
	return "", errors.New("未找到随包扫描器。请确认 scanner 文件夹与配装器 EXE 位于同一目录，且文件夹内包含 ZZZ-Scanner.Next.exe")
}

func scannerBundleFile(root, relative string) (string, error) {
	relative = filepath.Clean(filepath.FromSlash(relative))
	if relative == "." || filepath.IsAbs(relative) {
		return "", fmt.Errorf("扫描器清单包含非法路径 %q", relative)
	}
	full := filepath.Join(root, relative)
	rel, err := filepath.Rel(root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("扫描器清单路径越界 %q", relative)
	}
	return full, nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func verifyScannerBundle(root string) (scannerBundleManifest, error) {
	var manifest scannerBundleManifest
	manifestPath := filepath.Join(root, "SCANNER_BUNDLE.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return manifest, fmt.Errorf("无法读取扫描器清单: %w", err)
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return manifest, fmt.Errorf("扫描器清单格式错误: %w", err)
	}
	if strings.TrimSpace(manifest.Version) == "" || len(manifest.Files) == 0 {
		return manifest, errors.New("扫描器清单缺少版本或文件校验信息")
	}
	for relative, expected := range manifest.Files {
		path, err := scannerBundleFile(root, relative)
		if err != nil {
			return manifest, err
		}
		actual, err := fileSHA256(path)
		if err != nil {
			return manifest, fmt.Errorf("扫描器文件缺失或无法读取 %s: %w", relative, err)
		}
		if !strings.EqualFold(actual, strings.TrimSpace(expected)) {
			return manifest, fmt.Errorf("扫描器文件校验失败 %s", relative)
		}
	}
	return manifest, nil
}

func handleScannerStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if runtime.GOOS != "windows" {
		writeError(w, http.StatusBadRequest, "随包扫描器仅支持 Windows x64")
		return
	}

	scannerRuntime.Lock()
	if scannerRuntime.cmd != nil {
		pid := scannerRuntime.cmd.Process.Pid
		scannerRuntime.Unlock()
		writeJSON(w, map[string]any{"ok": true, "alreadyRunning": true, "pid": pid})
		return
	}

	root, err := findScannerBundle()
	if err != nil {
		scannerRuntime.Unlock()
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	manifest, err := verifyScannerBundle(root)
	if err != nil {
		scannerRuntime.Unlock()
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	configDir, err := appConfigDir()
	if err != nil {
		scannerRuntime.Unlock()
		writeError(w, http.StatusInternalServerError, "无法创建扫描结果目录: "+err.Error())
		return
	}
	outputRoot := filepath.Join(configDir, "scanner-outputs")
	if err := os.MkdirAll(outputRoot, 0755); err != nil {
		scannerRuntime.Unlock()
		writeError(w, http.StatusInternalServerError, "无法创建扫描结果目录: "+err.Error())
		return
	}

	executable := filepath.Join(root, "ZZZ-Scanner.Next.exe")
	cmd := exec.Command(executable)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "ZZZ_SCANNER_OUTPUT_ROOT="+outputRoot)
	if err := cmd.Start(); err != nil {
		scannerRuntime.Unlock()
		writeError(w, http.StatusInternalServerError, "扫描器启动失败: "+err.Error())
		return
	}
	scannerRuntime.cmd = cmd
	scannerRuntime.Unlock()

	go func(started *exec.Cmd) {
		err := started.Wait()
		scannerRuntime.Lock()
		if scannerRuntime.cmd == started {
			scannerRuntime.cmd = nil
		}
		scannerRuntime.Unlock()
		if err != nil {
			log.Printf("扫描器进程已退出: %v", err)
		}
	}(cmd)

	writeJSON(w, map[string]any{
		"ok":              true,
		"alreadyRunning":  false,
		"pid":             cmd.Process.Pid,
		"scannerVersion":  manifest.Version,
		"outputDirectory": outputRoot,
	})
}

func chooseStorageFolder(initial string) (string, bool, error) {
	if runtime.GOOS == "windows" {
		ps, err := exec.LookPath("powershell.exe")
		if err != nil {
			ps, err = exec.LookPath("powershell")
		}
		if err != nil {
			return "", false, errors.New("未找到 PowerShell，无法打开文件夹选择窗口。")
		}
		script := `$ErrorActionPreference = 'Stop'
$utf8NoBom = New-Object System.Text.UTF8Encoding $false
[Console]::OutputEncoding = $utf8NoBom
$OutputEncoding = $utf8NoBom
Add-Type -AssemblyName System.Windows.Forms
$dialog = New-Object System.Windows.Forms.FolderBrowserDialog
$dialog.Description = '选择 ZZZ Drive Optimizer 库存保存文件夹'
$dialog.ShowNewFolderButton = $true
$initial = $env:ZZZ_STORAGE_INITIAL
if (![string]::IsNullOrWhiteSpace($initial) -and [System.IO.Directory]::Exists($initial)) { $dialog.SelectedPath = $initial }
$result = $dialog.ShowDialog()
if ($result -eq [System.Windows.Forms.DialogResult]::OK) { Write-Output $dialog.SelectedPath }
`
		encoded := encodePowerShellCommand(script)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, ps, "-NoProfile", "-STA", "-ExecutionPolicy", "Bypass", "-EncodedCommand", encoded)
		cmd.Env = append(os.Environ(), "ZZZ_STORAGE_INITIAL="+initial)
		out, err := cmd.CombinedOutput()
		text := strings.TrimSpace(string(out))
		if ctx.Err() != nil {
			return "", false, errors.New("文件夹选择窗口超时。")
		}
		if err != nil {
			return "", false, fmt.Errorf("打开文件夹选择窗口失败：%v %s", err, text)
		}
		if text == "" {
			return "", true, nil
		}
		lines := nonEmptyLines(text)
		if len(lines) > 0 {
			return lines[len(lines)-1], false, nil
		}
		return text, false, nil
	}

	// Development fallback. The published Windows EXE uses the native folder picker above.
	if strings.TrimSpace(initial) != "" {
		return initial, false, nil
	}
	return "", false, errors.New("当前系统暂不支持可视化文件夹选择。")
}

type StorageFolderResponse struct {
	StateResponse
	Cancelled bool `json:"cancelled"`
}

func handleStorageFolder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	def, err := defaultStoragePath()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法确定默认路径: "+err.Error())
		return
	}
	srvState.mu.RLock()
	initial := srvState.storagePath
	if strings.TrimSpace(initial) == "" {
		initial = def
	}
	srvState.mu.RUnlock()
	if strings.ToLower(filepath.Ext(initial)) == ".json" {
		initial = filepath.Dir(initial)
	}
	folder, cancelled, err := chooseStorageFolder(initial)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if cancelled || strings.TrimSpace(folder) == "" {
		srvState.mu.RLock()
		resp := StorageFolderResponse{StateResponse: StateResponse{
			Version:                 srvState.state.Version,
			Discs:                   append([]Disc{}, srvState.state.Discs...),
			SetEffects:              append([]SetEffect{}, srvState.state.SetEffects...),
			CharacterBuilds:         append([]CharacterBuild{}, srvState.state.CharacterBuilds...),
			DiscClaims:              append([]DiscClaim{}, srvState.state.DiscClaims...),
			ClaimsInitialized:       srvState.state.ClaimsInitialized,
			StoragePath:             srvState.storagePath,
			DefaultStoragePath:      def,
			UsingDefaultStoragePath: sameStoragePath(srvState.storagePath, def),
		}, Cancelled: true}
		srvState.mu.RUnlock()
		writeJSON(w, resp)
		return
	}
	target, err := normalizeUserStoragePath(folder)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := ensureStoragePath(target); err != nil {
		writeError(w, http.StatusInternalServerError, "创建存储目录失败: "+err.Error())
		return
	}
	srvState.mu.Lock()
	current := srvState.state
	if err := saveState(target, current); err != nil {
		srvState.mu.Unlock()
		writeError(w, http.StatusInternalServerError, "写入新存储路径失败: "+err.Error())
		return
	}
	if sameStoragePath(target, def) {
		if err := clearStorageConfig(); err != nil {
			srvState.mu.Unlock()
			writeError(w, http.StatusInternalServerError, "恢复默认配置失败: "+err.Error())
			return
		}
	} else {
		if err := saveStorageConfig(target); err != nil {
			srvState.mu.Unlock()
			writeError(w, http.StatusInternalServerError, "保存路径配置失败: "+err.Error())
			return
		}
	}
	srvState.storagePath = target
	resp := StorageFolderResponse{StateResponse: StateResponse{
		Version:                 srvState.state.Version,
		Discs:                   append([]Disc{}, srvState.state.Discs...),
		SetEffects:              append([]SetEffect{}, srvState.state.SetEffects...),
		CharacterBuilds:         append([]CharacterBuild{}, srvState.state.CharacterBuilds...),
		DiscClaims:              append([]DiscClaim{}, srvState.state.DiscClaims...),
		ClaimsInitialized:       srvState.state.ClaimsInitialized,
		StoragePath:             srvState.storagePath,
		DefaultStoragePath:      def,
		UsingDefaultStoragePath: sameStoragePath(srvState.storagePath, def),
	}, Cancelled: false}
	srvState.mu.Unlock()
	writeJSON(w, resp)
}

func handleStoragePath(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req StoragePathRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "JSON 格式错误: "+err.Error())
		return
	}
	def, err := defaultStoragePath()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法确定默认路径: "+err.Error())
		return
	}
	target := def
	if !req.Reset {
		target, err = normalizeUserStoragePath(req.Path)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if err := ensureStoragePath(target); err != nil {
		writeError(w, http.StatusInternalServerError, "创建存储目录失败: "+err.Error())
		return
	}
	srvState.mu.Lock()
	current := srvState.state
	if err := saveState(target, current); err != nil {
		srvState.mu.Unlock()
		writeError(w, http.StatusInternalServerError, "写入新存储路径失败: "+err.Error())
		return
	}
	if req.Reset || sameStoragePath(target, def) {
		if err := clearStorageConfig(); err != nil {
			srvState.mu.Unlock()
			writeError(w, http.StatusInternalServerError, "恢复默认配置失败: "+err.Error())
			return
		}
	} else {
		if err := saveStorageConfig(target); err != nil {
			srvState.mu.Unlock()
			writeError(w, http.StatusInternalServerError, "保存路径配置失败: "+err.Error())
			return
		}
	}
	srvState.storagePath = target
	resp := StateResponse{
		Version:                 srvState.state.Version,
		Discs:                   append([]Disc{}, srvState.state.Discs...),
		SetEffects:              append([]SetEffect{}, srvState.state.SetEffects...),
		CharacterBuilds:         append([]CharacterBuild{}, srvState.state.CharacterBuilds...),
		DiscClaims:              append([]DiscClaim{}, srvState.state.DiscClaims...),
		ClaimsInitialized:       srvState.state.ClaimsInitialized,
		StoragePath:             srvState.storagePath,
		DefaultStoragePath:      def,
		UsingDefaultStoragePath: sameStoragePath(srvState.storagePath, def),
	}
	srvState.mu.Unlock()
	writeJSON(w, resp)
}

func handleOptimize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req OptimizeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "JSON 格式错误: "+err.Error())
		return
	}
	if req.Discs == nil {
		srvState.mu.RLock()
		req.Discs = append([]Disc{}, srvState.state.Discs...)
		req.SetEffects = append([]SetEffect{}, srvState.state.SetEffects...)
		srvState.mu.RUnlock()
	}
	ctx, cancel := context.WithCancel(r.Context())
	runID := time.Now().UnixNano()
	optimizerState.mu.Lock()
	if optimizerState.cancel != nil {
		optimizerState.cancel()
	}
	optimizerState.cancel = cancel
	optimizerState.started = time.Now()
	optimizerState.id = runID
	optimizerState.mu.Unlock()
	defer func() {
		optimizerState.mu.Lock()
		if optimizerState.id == runID {
			optimizerState.cancel = nil
			optimizerState.started = time.Time{}
			optimizerState.id = 0
		}
		optimizerState.mu.Unlock()
		cancel()
	}()
	resp := optimize(ctx, req)
	writeJSON(w, resp)
}

func handleCancelOptimize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	optimizerState.mu.Lock()
	cancel := optimizerState.cancel
	started := optimizerState.started
	if cancel != nil {
		cancel()
	}
	optimizerState.mu.Unlock()
	if cancel == nil {
		writeJSON(w, map[string]any{"ok": false, "message": "当前没有正在运行的配装计算。"})
		return
	}
	msg := "已发送强制终止指令，当前计算会在最近的安全检查点停止。"
	if !started.IsZero() {
		msg = fmt.Sprintf("已发送强制终止指令，本次计算已运行 %.1f 秒，会在最近的安全检查点停止。", time.Since(started).Seconds())
	}
	writeJSON(w, map[string]any{"ok": true, "message": msg})
}

func handleOCR(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 12<<20)
	if err := r.ParseMultipartForm(12 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "图片上传失败："+err.Error())
		return
	}
	file, header, err := r.FormFile("image")
	if err != nil {
		writeError(w, http.StatusBadRequest, "请上传图片文件，字段名为 image。")
		return
	}
	defer file.Close()
	ext := strings.ToLower(filepath.Ext(header.Filename))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".webp", ".bmp":
	default:
		ext = ".png"
	}
	tmp, err := os.CreateTemp("", "zzz-drive-ocr-*"+ext)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "创建临时图片失败："+err.Error())
		return
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := io.Copy(tmp, file); err != nil {
		_ = tmp.Close()
		writeError(w, http.StatusInternalServerError, "保存临时图片失败："+err.Error())
		return
	}
	if err := tmp.Close(); err != nil {
		writeError(w, http.StatusInternalServerError, "写入临时图片失败："+err.Error())
		return
	}

	rawText, engine, err := runOCR(tmpPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp := parseOCRText(rawText)
	resp.Engine = engine
	writeJSON(w, resp)
}

func handleOCRParse(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req OCRParseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "OCR 原文 JSON 格式错误: "+err.Error())
		return
	}
	if strings.TrimSpace(req.RawText) == "" {
		writeError(w, http.StatusBadRequest, "OCR 原文为空。")
		return
	}
	resp := parseOCRText(req.RawText)
	resp.Engine = strings.TrimSpace(req.Engine)
	if resp.Engine == "" {
		resp.Engine = "浏览器 OCR"
	}
	writeJSON(w, resp)
}

func runOCR(path string) (string, string, error) {
	attempts := []string{}
	if text, err := runTesseractOCR(path); err == nil && strings.TrimSpace(text) != "" {
		return text, "Tesseract", nil
	} else if err != nil {
		attempts = append(attempts, err.Error())
	}
	if runtime.GOOS == "windows" {
		if text, err := runWindowsOCR(path); err == nil && strings.TrimSpace(text) != "" {
			return text, "Windows OCR", nil
		} else if err != nil {
			attempts = append(attempts, err.Error())
		}
	}
	msg := "没有可用的 OCR 引擎。当前版本会优先使用系统 PATH 中的 Tesseract；Windows 版会再尝试系统自带 Windows OCR。请确认图片清晰，或安装中文 OCR 支持后重试。"
	if len(attempts) > 0 {
		msg += "\n" + strings.Join(attempts, "\n")
	}
	return "", "", errors.New(msg)
}

func runTesseractOCR(path string) (string, error) {
	bin, err := findTesseract()
	if err != nil {
		return "", err
	}
	langs := []string{"HanS+eng", "chi_sim+eng", "chi_tra+eng", "eng"}
	psms := []string{"6", "11", "12", "4"}
	bestText := ""
	bestScore := -1
	var lastErr string
	for _, lang := range langs {
		for _, psm := range psms {
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			cmd := exec.CommandContext(ctx, bin, path, "stdout", "-l", lang, "--psm", psm)
			out, err := cmd.CombinedOutput()
			cancel()
			text := strings.TrimSpace(string(out))
			if err == nil && text != "" {
				score := ocrTextQualityScore(text)
				if score > bestScore || (score == bestScore && len(text) > len(bestText)) {
					bestScore = score
					bestText = text
				}
				if score >= 13 {
					return text, nil
				}
				continue
			}
			if ctx.Err() != nil {
				lastErr = "Tesseract 识别超时。"
			} else if err != nil {
				lastErr = fmt.Sprintf("Tesseract[%s psm %s] 失败：%v %s", lang, psm, err, strings.TrimSpace(string(out)))
			}
		}
	}
	if strings.TrimSpace(bestText) != "" {
		return bestText, nil
	}
	if lastErr == "" {
		lastErr = "Tesseract 未输出可用文字。"
	}
	return "", errors.New(lastErr)
}

func ocrTextQualityScore(raw string) int {
	resp := parseOCRText(raw)
	score := 0
	if strings.TrimSpace(resp.SetName) != "" {
		score += 2
	}
	if resp.Slot >= 1 && resp.Slot <= 6 {
		score += 2
	}
	if strings.TrimSpace(resp.MainStat.Type) != "" {
		score += 3
	}
	score += len(resp.SubStats) * 2
	if resp.Level >= 0 && resp.Level <= 15 {
		score++
	}
	return score
}

func findTesseract() (string, error) {
	if bin, err := exec.LookPath("tesseract"); err == nil {
		return bin, nil
	}
	if runtime.GOOS == "windows" {
		candidates := []string{
			filepath.Join(os.Getenv("ProgramFiles"), "Tesseract-OCR", "tesseract.exe"),
			filepath.Join(os.Getenv("ProgramFiles(x86)"), "Tesseract-OCR", "tesseract.exe"),
			filepath.Join(os.Getenv("LocalAppData"), "Programs", "Tesseract-OCR", "tesseract.exe"),
		}
		for _, candidate := range candidates {
			if strings.TrimSpace(candidate) == "" {
				continue
			}
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate, nil
			}
		}
	}
	return "", errors.New("未找到 Tesseract 命令。")
}

func runWindowsOCR(path string) (string, error) {
	ps, err := exec.LookPath("powershell.exe")
	if err != nil {
		ps, err = exec.LookPath("powershell")
	}
	if err != nil {
		return "", errors.New("未找到 PowerShell，无法调用 Windows OCR。")
	}

	// Windows PowerShell 5.1 reads .ps1 files using the system ANSI code page unless
	// the file has a BOM. The v5 implementation wrote a UTF-8 script to a temp file,
	// which could make the Chinese strings mojibake and even break parsing on some
	// machines. Use -EncodedCommand instead; PowerShell requires UTF-16LE Base64, so
	// the script is decoded unambiguously on every Windows locale. Keep the script's
	// own throw strings ASCII-only to make diagnostic output safer as well.
	script := `$ErrorActionPreference = 'Stop'
$utf8NoBom = New-Object System.Text.UTF8Encoding $false
[Console]::OutputEncoding = $utf8NoBom
$OutputEncoding = $utf8NoBom
$imagePath = $env:ZZZ_OCR_IMAGE_PATH
if ([string]::IsNullOrWhiteSpace($imagePath)) { throw 'ZZZ_OCR_IMAGE_PATH is empty.' }
Add-Type -AssemblyName System.Runtime.WindowsRuntime
function AwaitOp($operation, [type]$resultType) {
    $asTaskGeneric = ([System.WindowsRuntimeSystemExtensions].GetMethods() | Where-Object {
        $_.Name -eq 'AsTask' -and $_.IsGenericMethodDefinition -and $_.GetParameters().Count -eq 1 -and $_.GetParameters()[0].ParameterType.Name -eq 'IAsyncOperation` + "`" + `1'
    } | Select-Object -First 1)
    if ($null -eq $asTaskGeneric) { throw 'WindowsRuntime AsTask bridge is not available.' }
    $asTask = $asTaskGeneric.MakeGenericMethod($resultType)
    $task = $asTask.Invoke($null, @($operation))
    $task.Wait()
    if ($task.Exception) { throw $task.Exception }
    return $task.Result
}
[Windows.Storage.StorageFile, Windows.Storage, ContentType = WindowsRuntime] | Out-Null
[Windows.Storage.Streams.IRandomAccessStreamWithContentType, Windows.Storage.Streams, ContentType = WindowsRuntime] | Out-Null
[Windows.Graphics.Imaging.BitmapDecoder, Windows.Graphics.Imaging, ContentType = WindowsRuntime] | Out-Null
[Windows.Graphics.Imaging.SoftwareBitmap, Windows.Graphics.Imaging, ContentType = WindowsRuntime] | Out-Null
[Windows.Graphics.Imaging.BitmapPixelFormat, Windows.Graphics.Imaging, ContentType = WindowsRuntime] | Out-Null
[Windows.Graphics.Imaging.BitmapAlphaMode, Windows.Graphics.Imaging, ContentType = WindowsRuntime] | Out-Null
[Windows.Media.Ocr.OcrEngine, Windows.Foundation, ContentType = WindowsRuntime] | Out-Null
[Windows.Media.Ocr.OcrResult, Windows.Foundation, ContentType = WindowsRuntime] | Out-Null
[Windows.Globalization.Language, Windows.Globalization, ContentType = WindowsRuntime] | Out-Null
$file = AwaitOp ([Windows.Storage.StorageFile]::GetFileFromPathAsync($imagePath)) ([Windows.Storage.StorageFile])
$stream = AwaitOp ($file.OpenReadAsync()) ([Windows.Storage.Streams.IRandomAccessStreamWithContentType])
$decoder = AwaitOp ([Windows.Graphics.Imaging.BitmapDecoder]::CreateAsync($stream)) ([Windows.Graphics.Imaging.BitmapDecoder])
$bitmap = AwaitOp ($decoder.GetSoftwareBitmapAsync()) ([Windows.Graphics.Imaging.SoftwareBitmap])
$bitmap = [Windows.Graphics.Imaging.SoftwareBitmap]::Convert($bitmap, [Windows.Graphics.Imaging.BitmapPixelFormat]::Bgra8, [Windows.Graphics.Imaging.BitmapAlphaMode]::Premultiplied)
$engine = $null
foreach ($availableLanguage in [Windows.Media.Ocr.OcrEngine]::AvailableRecognizerLanguages) {
    if ($availableLanguage.LanguageTag -like 'zh*') {
        $engine = [Windows.Media.Ocr.OcrEngine]::TryCreateFromLanguage($availableLanguage)
        if ($null -ne $engine) { break }
    }
}
if ($null -eq $engine) {
    foreach ($tag in @('zh-Hans-CN', 'zh-Hans', 'zh-CN', 'zh-Hant-TW', 'zh-Hant')) {
        try {
            $tryLang = New-Object Windows.Globalization.Language $tag
            $engine = [Windows.Media.Ocr.OcrEngine]::TryCreateFromLanguage($tryLang)
            if ($null -ne $engine) { break }
        } catch { }
    }
}
if ($null -eq $engine) { $engine = [Windows.Media.Ocr.OcrEngine]::TryCreateFromUserProfileLanguages() }
if ($null -eq $engine) { throw 'Windows OCR has no available language. Install Chinese OCR/language support in Windows Settings.' }
$result = AwaitOp ($engine.RecognizeAsync($bitmap)) ([Windows.Media.Ocr.OcrResult])
Write-Output $result.Text
`

	encoded := encodePowerShellCommand(script)
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ps, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-EncodedCommand", encoded)
	cmd.Env = append(os.Environ(), "ZZZ_OCR_IMAGE_PATH="+path)
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if ctx.Err() != nil {
		return "", errors.New("Windows OCR 识别超时。")
	}
	if err != nil {
		return "", fmt.Errorf("Windows OCR 失败：%v %s", err, text)
	}
	if text == "" {
		return "", errors.New("Windows OCR 未输出可用文字。")
	}
	return text, nil
}

func encodePowerShellCommand(script string) string {
	encoded := utf16.Encode([]rune(script))
	buf := make([]byte, len(encoded)*2)
	for i, v := range encoded {
		buf[i*2] = byte(v)
		buf[i*2+1] = byte(v >> 8)
	}
	return base64.StdEncoding.EncodeToString(buf)
}

func parseOCRText(raw string) OCRResponse {
	resp := OCRResponse{RawText: raw, Rarity: "S", Level: 15, SubStats: []StatValue{}, Warnings: []string{}, DoubtfulFields: []string{}}
	text := normalizeOCRText(raw)
	lines := nonEmptyLines(text)
	slot, slotExplicit := parseOCRSlotCandidate(lines, text)
	resp.Slot = slot
	setName, rawSet, matched := parseOCRSetName(lines)
	resp.SetName = setName
	if rawSet == "" {
		addOCRDoubt(&resp, "setName", "没有可靠识别到套装名，请手动确认。")
	} else if matched && setName != rawSet {
		addOCRDoubt(&resp, "setName", fmt.Sprintf("套装名 OCR 为「%s」，已按内置列表匹配为「%s」，请确认。", rawSet, setName))
	}
	if lv, ok := parseOCRLevel(text); ok && lv != 15 {
		addOCRDoubt(&resp, "level", fmt.Sprintf("OCR 识别到等级 %d，但当前版本默认按 S 级 +15 驱动盘录入；已按 +15 处理。", lv))
	}
	resp.Rarity = "S"
	resp.Level = 15
	section := ""
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		compact := compactHan(line)
		if strings.Contains(compact, "主属性") {
			section = "main"
			rest := strings.TrimSpace(strings.Replace(line, "主属性", "", 1))
			if rest == "" {
				continue
			}
			line = rest
		}
		if strings.Contains(compactHan(line), "副属性") {
			section = "sub"
			rest := strings.TrimSpace(strings.Replace(line, "副属性", "", 1))
			if rest == "" {
				continue
			}
			line = rest
		}
		if section == "sub" && ocrLineEndsSubSection(line) {
			section = ""
			continue
		}
		stat, ok := parseOCRStatLine(line)
		if !ok && i+1 < len(lines) && looksLikeOCRValueOnly(lines[i+1]) {
			combined := strings.TrimSpace(line + " " + lines[i+1])
			stat, ok = parseOCRStatLine(combined)
			if ok {
				stat.Raw = combined
				i++
			} else if section == "sub" && len(resp.SubStats) < 4 {
				if unknown, hasValue := unknownOCRSubStat(combined); hasValue {
					resp.SubStats = append(resp.SubStats, unknown)
					i++
					continue
				}
			}
		}
		if !ok && section == "main" && strings.TrimSpace(resp.MainStat.Type) == "" {
			if labelStat, labelOK := parseOCRMainLabelOnly(line, resp.Slot); labelOK {
				resp.MainStat = labelStat
				addOCRDoubt(&resp, "mainStat", fmt.Sprintf("主属性仅识别到标签「%s」，已按默认 S 级 15 级数值填入，请确认。", strings.TrimSpace(line)))
				continue
			}
		}
		if !ok {
			if section == "sub" && len(resp.SubStats) < 4 && looksLikeUnresolvedOCRSubStat(line) {
				if unknown, hasValue := unknownOCRSubStat(line); hasValue {
					resp.SubStats = append(resp.SubStats, unknown)
				}
			}
			continue
		}
		if strings.TrimSpace(stat.Raw) == "" {
			stat.Raw = line
		}
		if section == "main" && strings.TrimSpace(resp.MainStat.Type) == "" {
			resp.MainStat = stat
			continue
		}
		if section == "sub" && len(resp.SubStats) < 4 {
			resp.SubStats = append(resp.SubStats, stat)
		}
	}
	// Windows OCR 有时会把整个属性区域塞进一行，例如：
	// “主属性 攻击力 316 副属性 暴击率 +2 7.2% ...”。
	// 逐行解析在这种情况下会丢属性，所以这里再做一次全文扫描，
	// 按“主属性 / 副属性”标记和属性名 + 数值重新切分。
	applyScannedOCRStats(&resp, text)
	reconcileOCRSlotWithMainStat(&resp, slotExplicit)
	validateOCRMainStatValue(&resp)
	markOCRDoubts(&resp)

	if resp.Slot == 0 {
		addOCRDoubt(&resp, "slot", "没有可靠识别到槽位，请手动选择 1-6 号位。")
	}
	if strings.TrimSpace(resp.MainStat.Type) == "" {
		addOCRDoubt(&resp, "mainStat", "没有识别到主属性，请手动确认。")
	} else if resp.Slot >= 1 && resp.Slot <= 6 {
		if allowed := ocrSlotMainAllowed[resp.Slot]; len(allowed) > 0 && !allowed[resp.MainStat.Type] {
			msg := fmt.Sprintf("识别到 %d 号位主属性为「%s」，不在该槽位主属性池内，请确认槽位或主属性。", resp.Slot, statCNName(resp.MainStat.Type))
			addOCRDoubt(&resp, "slot", msg)
			addOCRDoubt(&resp, "mainStat", msg)
		}
	}
	if len(resp.SubStats) < 4 {
		addOCRDoubt(&resp, "subStats", fmt.Sprintf("仅定位到 %d 条副词条位置，请检查图片或手动补充。", len(resp.SubStats)))
		for i := len(resp.SubStats); i < 4; i++ {
			addOCRDoubt(&resp, fmt.Sprintf("subStats.%d.type", i), "")
			addOCRDoubt(&resp, fmt.Sprintf("subStats.%d.value", i), "")
		}
	}
	unresolved := 0
	seen := map[string]bool{}
	for i, s := range resp.SubStats {
		if s.Type == "" {
			unresolved++
			addOCRDoubt(&resp, fmt.Sprintf("subStats.%d.type", i), fmt.Sprintf("第 %d 条副词条的属性名未可靠识别；已保留该位置和数值，请在保存前从下拉菜单手动选择。", i+1))
			continue
		}
		if s.Suspect {
			addOCRDoubt(&resp, fmt.Sprintf("subStats.%d.type", i), fmt.Sprintf("第 %d 条副词条「%s」识别存疑，请确认。", i+1, statCNName(s.Type)))
		}
		if s.Type == resp.MainStat.Type {
			addOCRDoubt(&resp, fmt.Sprintf("subStats.%d.type", i), "识别结果中副词条与主属性重复，请手动确认。")
		}
		if seen[s.Type] {
			addOCRDoubt(&resp, fmt.Sprintf("subStats.%d.type", i), "识别结果中出现重复副词条，请手动确认。")
		}
		seen[s.Type] = true
	}
	if unresolved > 0 {
		addOCRDoubt(&resp, "subStats", fmt.Sprintf("有 %d 条副词条保留了原位置，但属性名未识别；请在下方表单对应行手动选择属性。", unresolved))
	}
	return resp
}

func addDoubtfulField(resp *OCRResponse, field string) {
	if resp == nil || strings.TrimSpace(field) == "" {
		return
	}
	for _, f := range resp.DoubtfulFields {
		if f == field {
			return
		}
	}
	resp.DoubtfulFields = append(resp.DoubtfulFields, field)
}

func addOCRDoubt(resp *OCRResponse, field, message string) {
	addDoubtfulField(resp, field)
	if resp == nil || strings.TrimSpace(message) == "" {
		return
	}
	for _, w := range resp.Warnings {
		if w == message {
			return
		}
	}
	resp.Warnings = append(resp.Warnings, message)
}

func uniqueOCRSlotForMainStat(statType string) (int, bool) {
	switch statType {
	case "HP_FLAT":
		return 1, true
	case "ATK_FLAT":
		return 2, true
	case "DEF_FLAT":
		return 3, true
	case "CRIT_RATE", "CRIT_DMG", "ANOMALY_PROFICIENCY":
		return 4, true
	case "PEN_RATIO", "FIRE_DMG", "ICE_DMG", "ELECTRIC_DMG", "PHYSICAL_DMG", "ETHER_DMG", "WIND_DMG", "LUMIFLUX_DMG":
		return 5, true
	case "ANOMALY_MASTERY", "ENERGY_REGEN", "IMPACT":
		return 6, true
	}
	return 0, false
}

func reconcileOCRSlotWithMainStat(resp *OCRResponse, slotExplicit bool) {
	if resp == nil {
		return
	}
	mainType := strings.TrimSpace(resp.MainStat.Type)
	if mainType == "" {
		return
	}
	if inferred, ok := uniqueOCRSlotForMainStat(mainType); ok {
		if resp.Slot == 0 {
			resp.Slot = inferred
			addOCRDoubt(resp, "slot", fmt.Sprintf("未可靠识别到槽位；已根据主属性「%s」推断为 %d 号位，请确认。", statCNName(mainType), inferred))
			return
		}
		if resp.Slot != inferred {
			old := resp.Slot
			resp.Slot = inferred
			resp.MainStat.Suspect = true
			if slotExplicit {
				addOCRDoubt(resp, "slot", fmt.Sprintf("OCR 槽位疑似为 %d 号位，但主属性「%s」只会出现在 %d 号位；已临时改为 %d 号位并标红，请确认。", old, statCNName(mainType), inferred, inferred))
			} else {
				addOCRDoubt(resp, "slot", fmt.Sprintf("槽位识别不稳定；已根据主属性「%s」临时推断为 %d 号位，请确认。", statCNName(mainType), inferred))
			}
			addDoubtfulField(resp, "mainStat")
		}
		return
	}
	if resp.Slot >= 1 && resp.Slot <= 6 {
		if allowed := ocrSlotMainAllowed[resp.Slot]; len(allowed) > 0 && !allowed[mainType] {
			resp.MainStat.Suspect = true
			addOCRDoubt(resp, "slot", fmt.Sprintf("槽位与主属性池不一致：%d 号位通常不能出现主属性「%s」；可能槽位或主属性识别有误。", resp.Slot, statCNName(mainType)))
			addDoubtfulField(resp, "mainStat")
		}
	}
}

func parseOCRMainLabelOnly(line string, slot int) (StatValue, bool) {
	line = strings.TrimSpace(normalizeOCRText(line))
	if line == "" {
		return StatValue{}, false
	}
	if strings.Contains(compactHan(line), "副属性") || strings.Contains(compactHan(line), "套装效果") || strings.Contains(compactHan(line), "等级") || strings.Contains(compactHan(line), "查看") {
		return StatValue{}, false
	}
	if regexp.MustCompile(`[0-9]`).MatchString(line) {
		return StatValue{}, false
	}
	candidates := []string{}
	for _, label := range ocrStatLabels {
		if strings.Contains(compactHan(line), compactHan(label)) || levenshteinRunes(compactHan(line), compactHan(label)) <= 1 || (compactHan(label) == "暴击率" && likelyCritRateOCRLabel(compactHan(line))) {
			if t := statTypeFromOCRLabel(label, strings.Contains(line, "%")); t != "" {
				candidates = append(candidates, t)
			}
		}
	}
	if t := statTypeFromOCRLabel(line, strings.Contains(line, "%")); t != "" {
		candidates = append(candidates, t)
	}
	if len(candidates) == 0 {
		return StatValue{}, false
	}
	allowed := map[string]bool{}
	if slot >= 1 && slot <= 6 {
		allowed = ocrSlotMainAllowed[slot]
	}
	seen := map[string]bool{}
	for _, t := range candidates {
		if seen[t] {
			continue
		}
		seen[t] = true
		if len(allowed) > 0 && !allowed[t] {
			continue
		}
		if expected, ok := ocrDefaultMainStatValue(t); ok {
			return StatValue{Type: t, Value: expected, Raw: line, Suspect: true}, true
		}
	}
	for _, t := range candidates {
		if _, unique := uniqueOCRSlotForMainStat(t); unique {
			if expected, ok := ocrDefaultMainStatValue(t); ok {
				return StatValue{Type: t, Value: expected, Raw: line, Suspect: true}, true
			}
		}
	}
	return StatValue{}, false
}

func validateOCRMainStatValue(resp *OCRResponse) {
	if resp == nil {
		return
	}
	mainType := strings.TrimSpace(resp.MainStat.Type)
	if mainType == "" {
		return
	}
	expected, ok := ocrDefaultMainStatValue(mainType)
	if !ok || expected <= 0 {
		return
	}
	actual := resp.MainStat.Value
	tol := ocrMainStatValueTolerance(expected)
	if math.Abs(actual-expected) <= tol {
		if math.Abs(actual-expected) > 0.0001 {
			resp.MainStat.Value = expected
			resp.MainStat.Suspect = true
			addOCRDoubt(resp, "mainStat", fmt.Sprintf("主属性「%s」数值 OCR 为 %.1f，已按默认 S 级 +15 主词条修正为 %.1f，请确认。", statCNName(mainType), actual, expected))
		}
		return
	}
	// 常见错误：4号位暴击率主属性 24% 被 OCR 误成 2.4%，或主属性数值被识别成副词条档位。
	// 因为本工具现在默认录入 S 级 +15 驱动盘，主属性数值可以按类型直接校准。
	resp.MainStat.Value = expected
	resp.MainStat.Suspect = true
	addOCRDoubt(resp, "mainStat", fmt.Sprintf("主属性「%s」数值 OCR 为 %.1f，不符合默认 S 级 +15 主词条标准值 %.1f；已临时修正为 %.1f，请保存前确认。", statCNName(mainType), actual, expected, expected))
}

func ocrMainStatValueTolerance(expected float64) float64 {
	if expected >= 1000 {
		return 3
	}
	if expected >= 90 {
		return 1.5
	}
	if expected >= 40 {
		return 0.5
	}
	return 0.25
}

func markOCRDoubts(resp *OCRResponse) {
	if resp == nil {
		return
	}
	if resp.Slot == 0 {
		addDoubtfulField(resp, "slot")
	}
	if strings.TrimSpace(resp.MainStat.Type) == "" {
		addDoubtfulField(resp, "mainStat")
	} else if ocrStatNeedsReview(resp.MainStat) {
		resp.MainStat.Suspect = true
		addDoubtfulField(resp, "mainStat")
	}
	for i := range resp.SubStats {
		if ocrStatNeedsReview(resp.SubStats[i]) {
			resp.SubStats[i].Suspect = true
			addDoubtfulField(resp, fmt.Sprintf("subStats.%d.type", i))
		}
		if resp.SubStats[i].Value <= 0 {
			addDoubtfulField(resp, fmt.Sprintf("subStats.%d.value", i))
		}
	}
}

func ocrStatNeedsReview(stat StatValue) bool {
	if strings.TrimSpace(stat.Type) == "" {
		return true
	}
	if stat.Suspect {
		return true
	}
	if strings.TrimSpace(stat.Raw) == "" {
		return false
	}
	return !ocrStatLabelMatchesTypeExactly(stat.Type, stat.Raw)
}

func ocrStatLabelMatchesTypeExactly(statType, raw string) bool {
	han := compactHan(raw)
	ascii := compactASCII(raw)
	if han == "" && ascii == "" {
		return false
	}
	switch statType {
	case "CRIT_RATE":
		return strings.Contains(han, "暴击率") || strings.Contains(han, "爆击率") || strings.Contains(ascii, "CRITRATE") || strings.Contains(ascii, "CRITR")
	case "CRIT_DMG":
		return strings.Contains(han, "暴击伤害") || strings.Contains(han, "暴伤") || strings.Contains(ascii, "CRITDMG") || strings.Contains(ascii, "DMG")
	case "ANOMALY_PROFICIENCY":
		return strings.Contains(han, "异常精通")
	case "ANOMALY_MASTERY":
		return strings.Contains(han, "异常掌控")
	case "ENERGY_REGEN":
		return strings.Contains(han, "能量自动回复") || strings.Contains(han, "能量回复")
	case "PEN_RATIO":
		return strings.Contains(han, "穿透率")
	case "PEN_FLAT":
		return strings.Contains(han, "穿透值") || strings.Contains(han, "穿透")
	case "FIRE_DMG":
		return strings.Contains(han, "火属性伤害") || strings.Contains(han, "火伤害")
	case "ICE_DMG":
		return strings.Contains(han, "冰属性伤害") || strings.Contains(han, "冰伤害")
	case "ELECTRIC_DMG":
		return strings.Contains(han, "电属性伤害") || strings.Contains(han, "电伤害")
	case "PHYSICAL_DMG":
		return strings.Contains(han, "物理属性伤害") || strings.Contains(han, "物理伤害")
	case "ETHER_DMG":
		return strings.Contains(han, "以太属性伤害") || strings.Contains(han, "以太伤害")
	case "WIND_DMG":
		return strings.Contains(han, "风属性伤害") || strings.Contains(han, "风伤害")
	case "LUMIFLUX_DMG":
		return strings.Contains(han, "流明属性伤害") || strings.Contains(han, "流明伤害")
	case "IMPACT":
		return strings.Contains(han, "冲击力")
	case "HP_FLAT", "HP_PERCENT":
		return strings.Contains(han, "生命值") || han == "生命" || strings.Contains(ascii, "HP")
	case "ATK_FLAT", "ATK_PERCENT":
		return strings.Contains(han, "攻击力") || han == "攻击" || strings.Contains(ascii, "ATK") || strings.Contains(ascii, "ATTACK")
	case "DEF_FLAT", "DEF_PERCENT":
		return strings.Contains(han, "防御力") || han == "防御" || strings.Contains(ascii, "DEF")
	}
	return true
}

func statCNName(statType string) string {
	switch statType {
	case "HP_FLAT":
		return "生命值"
	case "HP_PERCENT":
		return "生命值%"
	case "ATK_FLAT":
		return "攻击力"
	case "ATK_PERCENT":
		return "攻击力%"
	case "DEF_FLAT":
		return "防御力"
	case "DEF_PERCENT":
		return "防御力%"
	case "BASE_ATK":
		return "音擎/核心基础攻击力"
	case "BASE_HP":
		return "核心基础生命值"
	case "BASE_DEF":
		return "核心基础防御力"
	case "SHEER_FORCE", "SHEER_FORCE_FLAT":
		return "贯穿力"
	case "CRIT_RATE":
		return "暴击率"
	case "CRIT_DMG":
		return "暴击伤害"
	case "PEN_FLAT":
		return "穿透值"
	case "PEN_RATIO":
		return "穿透率"
	case "ANOMALY_PROFICIENCY":
		return "异常精通"
	case "ANOMALY_MASTERY":
		return "异常掌控"
	case "ENERGY_REGEN":
		return "能量自动回复"
	case "IMPACT":
		return "冲击力"
	case "FIRE_DMG":
		return "火属性伤害"
	case "ICE_DMG":
		return "冰属性伤害"
	case "ELECTRIC_DMG":
		return "电属性伤害"
	case "PHYSICAL_DMG":
		return "物理属性伤害"
	case "ETHER_DMG":
		return "以太属性伤害"
	case "WIND_DMG":
		return "风属性伤害"
	case "LUMIFLUX_DMG":
		return "流明属性伤害"
	}
	return statType
}

type ocrStatHit struct {
	Stat  StatValue
	Start int
	End   int
}

func applyScannedOCRStats(resp *OCRResponse, text string) {
	hits := scanOCRStatHits(text)
	if len(hits) == 0 {
		return
	}
	mainPos := firstOCRMarkerIndex(text, "主属性", "主属", "主屬")
	subPos := firstOCRMarkerIndex(text, "副属性", "副属", "副屬")
	stopPos := firstOCRMarkerIndex(text, "套装效果", "查看")

	if mainPos >= 0 {
		mainEnd := len(text)
		if subPos >= 0 && subPos > mainPos {
			mainEnd = subPos
		} else if stopPos >= 0 && stopPos > mainPos {
			mainEnd = stopPos
		}
		if h, ok := firstOCRStatHitInRange(hits, mainPos, mainEnd); ok {
			if resp.MainStat.Type == "" || ocrMainStatLooksWeak(resp.MainStat, resp.Slot) {
				resp.MainStat = h.Stat
			}
		}
	}

	if subPos >= 0 {
		subEnd := len(text)
		if stopPos >= 0 && stopPos > subPos {
			subEnd = stopPos
		}
		scannedSubs := ocrSubRowsInRange(text, subPos, subEnd, 4)
		if ocrSubRowsScore(scannedSubs) > ocrSubRowsScore(resp.SubStats) {
			resp.SubStats = scannedSubs
		} else if len(resp.SubStats) < 4 {
			resp.SubStats = mergeOCRSubStats(resp.SubStats, scannedSubs, resp.MainStat.Type)
		}
	}

	// Last resort: when section labels are missed, pick the first stat that is legal
	// for the detected slot as the main stat, then use following stats as substats.
	if resp.MainStat.Type == "" && resp.Slot >= 1 && resp.Slot <= 6 {
		allowed := ocrSlotMainAllowed[resp.Slot]
		for i, h := range hits {
			if allowed[h.Stat.Type] {
				resp.MainStat = h.Stat
				rest := []StatValue{}
				for _, h2 := range hits[i+1:] {
					if len(rest) >= 4 {
						break
					}
					if h2.Stat.Type == resp.MainStat.Type {
						continue
					}
					rest = append(rest, h2.Stat)
				}
				if len(rest) > len(resp.SubStats) {
					resp.SubStats = rest
				}
				break
			}
		}
	}
}

func ocrMainStatLooksWeak(stat StatValue, slot int) bool {
	if stat.Type == "" {
		return true
	}
	if slot >= 1 && slot <= 6 {
		allowed := ocrSlotMainAllowed[slot]
		if len(allowed) > 0 && !allowed[stat.Type] {
			return true
		}
	}
	return false
}

func firstOCRMarkerIndex(text string, markers ...string) int {
	best := -1
	for _, marker := range markers {
		if marker == "" {
			continue
		}
		if idx := strings.Index(text, marker); idx >= 0 && (best < 0 || idx < best) {
			best = idx
		}
	}
	return best
}

func firstOCRStatHitInRange(hits []ocrStatHit, start, end int) (ocrStatHit, bool) {
	for _, h := range hits {
		if h.Start >= start && h.Start < end {
			return h, true
		}
	}
	return ocrStatHit{}, false
}

func ocrStatHitsInRange(hits []ocrStatHit, start, end, limit int) []StatValue {
	out := []StatValue{}
	for _, h := range hits {
		if h.Start < start || h.Start >= end {
			continue
		}
		out = append(out, h.Stat)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

func ocrGenericLabelValueRegexp() *regexp.Regexp {
	// Allows OCR output such as "暴 击 率 +2 7.2%" or a whole-line result like
	// "副属性 暴击率+27.2% 暴击伤害+19.6%". The optional +N is the upgrade count.
	return regexp.MustCompile(`(?s)((?:[\p{Han}A-Za-z]\s*){1,12})(?:[+＋]\s*[0-9]\s*)?([0-9]{1,5}(?:[\.,][0-9]+)?\s*%?)`)
}

func statValueFromOCRParts(label, valueText, raw string) (StatValue, bool) {
	value, hasPercent, ok := parseOCRScannedNumber(valueText)
	if !ok {
		return StatValue{}, false
	}
	return buildOCRStatValue(label, raw, value, hasPercent), true
}

func buildOCRStatValue(label, raw string, value float64, percent bool) StatValue {
	directType := statTypeFromOCRLabel(label, percent)
	statType := directType
	if statType == "" {
		statType = inferStatTypeFromOCRValue(raw, value, percent)
	}
	stat := StatValue{Type: statType, Value: value, Raw: strings.TrimSpace(raw)}
	if stat.Type == "" || directType == "" {
		stat.Suspect = true
	}
	if stat.Type == "CRIT_RATE" && !ocrStatLabelMatchesTypeExactly("CRIT_RATE", label) {
		stat.Suspect = true
	}
	return stat
}

func ocrSubRowsInRange(text string, start, end, limit int) []StatValue {
	if start < 0 || end <= start || start >= len(text) {
		return nil
	}
	if end > len(text) {
		end = len(text)
	}
	segment := text[start:end]
	matches := ocrGenericLabelValueRegexp().FindAllStringSubmatchIndex(segment, -1)
	out := []StatValue{}
	seenRaw := map[string]bool{}
	for _, m := range matches {
		if len(m) < 6 {
			continue
		}
		raw := segment[m[0]:m[1]]
		label := segment[m[2]:m[3]]
		valueText := segment[m[4]:m[5]]
		if ocrLineEndsSubSection(raw) {
			continue
		}
		stat, ok := statValueFromOCRParts(label, valueText, raw)
		if !ok {
			continue
		}
		if stat.Type == "" && !looksLikeUnresolvedOCRSubStat(raw) {
			continue
		}
		key := fmt.Sprintf("%s|%.4f|%s", stat.Type, stat.Value, compactHan(raw)+compactASCII(raw))
		if seenRaw[key] {
			continue
		}
		seenRaw[key] = true
		out = append(out, stat)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

func ocrSubRowsScore(rows []StatValue) int {
	score := len(rows) * 10
	for _, s := range rows {
		if strings.TrimSpace(s.Type) != "" {
			score += 8
		}
		if s.Value != 0 {
			score++
		}
	}
	return score
}

func mergeOCRSubStats(existing, scanned []StatValue, mainType string) []StatValue {
	out := append([]StatValue{}, existing...)
	for _, s := range scanned {
		if len(out) >= 4 {
			break
		}
		if s.Type != "" && s.Type == mainType {
			continue
		}
		duplicate := false
		for _, e := range out {
			if e.Type == s.Type && math.Abs(e.Value-s.Value) < 0.001 {
				duplicate = true
				break
			}
			if s.Type == "" && e.Type == "" && strings.TrimSpace(e.Raw) == strings.TrimSpace(s.Raw) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			out = append(out, s)
		}
	}
	return out
}

func scanOCRStatHits(text string) []ocrStatHit {
	labels := []string{
		"能量自动回复", "物理属性伤害", "以太属性伤害", "火属性伤害", "冰属性伤害", "电属性伤害",
		"暴击伤害", "异常精通", "异常掌控", "能量回复", "生命值", "攻击力", "防御力", "暴击率", "爆击率", "暴击辛", "暴击幸", "暴击宰", "暴击车", "暴击半", "暴击丰", "暴击卒", "暴击事", "暴击守", "暴击卑", "暴击牵", "穿透率", "穿透值", "冲击力",
	}
	parts := make([]string, 0, len(labels))
	for _, label := range labels {
		parts = append(parts, regexp.QuoteMeta(label))
	}
	labelExpr := strings.Join(parts, "|")
	// Allow an optional upgrade marker between label and value:
	// 暴击伤害 +2 14.4% / 暴击伤害+214.4% / 攻击力+13%
	exactRe := regexp.MustCompile(`(?s)(` + labelExpr + `)\s*(?:[+＋]\s*[0-9]\s*)?([0-9]{1,5}(?:[\.,][0-9]+)?\s*%?)`)
	out := []ocrStatHit{}
	add := func(start, end int, label, valueText, raw string) {
		stat, ok := statValueFromOCRParts(label, valueText, raw)
		if !ok || stat.Type == "" {
			return
		}
		for _, h := range out {
			if h.Start == start && h.End == end && h.Stat.Type == stat.Type && math.Abs(h.Stat.Value-stat.Value) < 0.001 {
				return
			}
		}
		out = append(out, ocrStatHit{Stat: stat, Start: start, End: end})
	}
	for _, m := range exactRe.FindAllStringSubmatchIndex(text, -1) {
		if len(m) < 6 {
			continue
		}
		add(m[0], m[1], text[m[2]:m[3]], text[m[4]:m[5]], text[m[0]:m[1]])
	}
	// Generic pass: catches OCR variants of “暴击率” and other labels that are
	// visually close but not exactly in the dictionary.
	for _, m := range ocrGenericLabelValueRegexp().FindAllStringSubmatchIndex(text, -1) {
		if len(m) < 6 {
			continue
		}
		add(m[0], m[1], text[m[2]:m[3]], text[m[4]:m[5]], text[m[0]:m[1]])
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Start == out[j].Start {
			return out[i].End < out[j].End
		}
		return out[i].Start < out[j].Start
	})
	return out
}

func parseOCRScannedNumber(s string) (float64, bool, bool) {
	s = strings.TrimSpace(strings.NewReplacer("％", "%", "，", ".", ",", ".", " ", "").Replace(s))
	if s == "" {
		return 0, false, false
	}
	hasPercent := strings.Contains(s, "%")
	s = strings.TrimSuffix(s, "%")
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false, false
	}
	return v, hasPercent, true
}

func normalizeOCRText(s string) string {
	repl := strings.NewReplacer(
		"\r\n", "\n", "\r", "\n",
		"％", "%", "＋", "+", "：", ":", "【", "[", "】", "]", "（", "(", "）", ")", "〔", "[", "〕", "]",
		"罕", "岿", "寡", "岿", // 常见于“云岿如我”的 OCR 误读
		"擊", "击", "傷", "伤", "屬", "属", "異", "异", "會", "会", "裏", "里",
	)
	return repl.Replace(s)
}

func nonEmptyLines(text string) []string {
	parts := strings.Split(text, "\n")
	out := []string{}
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func parseOCRSlot(lines []string, text string) int {
	slot, _ := parseOCRSlotCandidate(lines, text)
	return slot
}

func parseOCRSlotCandidate(lines []string, text string) (int, bool) {
	bracketRe := regexp.MustCompile(`[\[\(]\s*([1-6])\s*[\]\)]`)
	for _, line := range lines {
		if m := bracketRe.FindStringSubmatch(line); m != nil {
			v, _ := strconv.Atoi(m[1])
			return v, true
		}
	}
	if m := bracketRe.FindStringSubmatch(text); m != nil {
		v, _ := strconv.Atoi(m[1])
		return v, true
	}
	cnRe := regexp.MustCompile(`([1-6])\s*(?:号位|號位|号|號)`)
	if m := cnRe.FindStringSubmatch(text); m != nil {
		v, _ := strconv.Atoi(m[1])
		return v, true
	}
	return 0, false
}

func parseOCRSetName(lines []string) (string, string, bool) {
	re := regexp.MustCompile(`[\[\(]\s*[1-6]\s*[\]\)]`)
	for _, line := range lines {
		loc := re.FindStringIndex(line)
		if loc == nil || loc[0] == 0 {
			continue
		}
		raw := compactHan(line[:loc[0]])
		if raw == "" {
			continue
		}
		best, matched := bestSetName(raw)
		return best, raw, matched
	}
	return "", "", false
}

func parseOCRLevel(text string) (int, bool) {
	re := regexp.MustCompile(`([0-9]{1,2})\s*/\s*([0-9]{1,2})`)
	matches := re.FindAllStringSubmatch(text, -1)
	for _, m := range matches {
		lv, _ := strconv.Atoi(m[1])
		maxLv, _ := strconv.Atoi(m[2])
		if lv >= 0 && lv <= 15 && maxLv >= lv && maxLv <= 15 {
			return lv, true
		}
	}
	return 15, false
}

func ocrLineEndsSubSection(line string) bool {
	han := compactHan(line)
	ascii := compactASCII(line)
	if han == "" && ascii == "" {
		return false
	}
	return strings.Contains(han, "套装效果") || strings.Contains(han, "查看") || strings.Contains(han, "删除") || strings.Contains(han, "锁") || strings.Contains(ascii, "DETAIL") || strings.Contains(ascii, "EMPTY")
}

func unknownOCRSubStat(raw string) (StatValue, bool) {
	value, percent, ok := extractOCRValue(raw)
	if !ok {
		return StatValue{}, false
	}
	stat := buildOCRStatValue(raw, raw, value, percent)
	stat.Suspect = true
	return stat, true
}

func looksLikeUnresolvedOCRSubStat(line string) bool {
	line = strings.TrimSpace(normalizeOCRText(line))
	if line == "" || ocrLineEndsSubSection(line) {
		return false
	}
	_, _, ok := extractOCRValue(line)
	if !ok {
		return false
	}
	han := compactHan(line)
	ascii := compactASCII(line)
	// In the sub-stat area, a line with a number and at least some text is very
	// likely one visual sub-stat row. Keep it as an unresolved row instead of
	// dropping it and shifting the rows below upward.
	return strings.Contains(line, "%") || len([]rune(han)) >= 2 || hasOCRAsciiStatHint(line) || ascii != ""
}

func looksLikeOCRValueOnly(line string) bool {
	line = strings.TrimSpace(normalizeOCRText(line))
	if line == "" {
		return false
	}
	return regexp.MustCompile(`^[+\s]*[0-9]+(?:\.[0-9]+)?%?$`).MatchString(line)
}

func parseOCRStatLine(line string) (StatValue, bool) {
	line = normalizeOCRText(line)
	han := compactHan(line)
	if han == "" && !hasOCRAsciiStatHint(line) {
		return StatValue{}, false
	}
	if strings.Contains(han, "主属性") || strings.Contains(han, "副属性") || strings.Contains(han, "套装效果") || strings.Contains(han, "等级") || strings.Contains(han, "查看") {
		return StatValue{}, false
	}
	value, hasPercent, ok := extractOCRValue(line)
	if !ok {
		return StatValue{}, false
	}
	stat := buildOCRStatValue(line, line, value, hasPercent)
	if stat.Type == "" {
		return StatValue{}, false
	}
	return stat, true
}

func hasOCRAsciiStatHint(line string) bool {
	ascii := compactASCII(line)
	if ascii == "" {
		return false
	}
	return strings.Contains(ascii, "REGS") || strings.Contains(ascii, "BGS") || strings.Contains(ascii, "CRIT") || strings.Contains(ascii, "DMG") || strings.Contains(ascii, "ATK") || strings.Contains(ascii, "HP") || strings.Contains(ascii, "DEF")
}

func extractOCRValue(line string) (float64, bool, bool) {
	line = strings.NewReplacer("％", "%", "＋", "+", "，", ".", ",", ".").Replace(line)
	compact := strings.Join(strings.Fields(line), "")
	if strings.Contains(compact, "+") {
		rePlus := regexp.MustCompile(`\+([0-9])([0-9]+(?:\.[0-9]+)?)(%)?$`)
		if m := rePlus.FindStringSubmatch(compact); m != nil {
			v, err := strconv.ParseFloat(m[2], 64)
			if err == nil {
				return v, m[3] == "%" || strings.Contains(line, "%"), true
			}
		}
	}
	re := regexp.MustCompile(`[0-9]+(?:\.[0-9]+)?%?`)
	tokens := re.FindAllString(line, -1)
	if len(tokens) == 0 {
		return 0, false, false
	}
	// A bare suffix like "暴击伤害 +2" is only the upgrade count, not the value.
	// Let the caller merge it with the next OCR line, which often contains "14.4%".
	if len(tokens) == 1 && !strings.Contains(line, "%") && regexp.MustCompile(`[+＋]\s*[0-9]\s*$`).MatchString(line) {
		return 0, false, false
	}
	tok := tokens[len(tokens)-1]
	hasPercent := strings.HasSuffix(tok, "%") || strings.HasSuffix(compact, "%") || strings.Contains(line, "%")
	tok = strings.TrimSuffix(tok, "%")
	v, err := strconv.ParseFloat(tok, 64)
	if err != nil {
		return 0, false, false
	}
	return v, hasPercent, true
}

func extractOCRUpgradeCount(line string) (int, bool) {
	line = normalizeOCRText(line)
	re := regexp.MustCompile(`[+＋]\s*([0-9])`)
	m := re.FindStringSubmatch(line)
	if m == nil {
		return 0, false
	}
	v, err := strconv.Atoi(m[1])
	if err != nil || v < 0 || v > 9 {
		return 0, false
	}
	return v, true
}

func approxStatValue(value, target float64) bool {
	return math.Abs(value-target) <= 0.06
}

func inferStatTypeFromOCRValue(label string, value float64, percent bool) string {
	label = normalizeOCRText(label)
	han := compactHan(label)
	ascii := compactASCII(label)
	hasCritHint := strings.Contains(han, "暴") || strings.Contains(han, "击") || strings.Contains(han, "率") || strings.Contains(ascii, "CR") || strings.Contains(ascii, "RATE")
	hasDmgHint := strings.Contains(han, "伤") || strings.Contains(han, "害") || strings.Contains(han, "傷") || strings.Contains(ascii, "DMG")
	if percent && hasCritHint && !hasDmgHint {
		return "CRIT_RATE"
	}
	if percent && hasCritHint && hasDmgHint {
		return "CRIT_DMG"
	}
	if percent {
		if upgrades, ok := extractOCRUpgradeCount(label); ok {
			rolls := float64(upgrades + 1)
			if approxStatValue(value, rolls*2.4) {
				return "CRIT_RATE"
			}
		}
		// Values like 2.4 / 7.2 / 12.0 are unique to crit-rate rolls among
		// common S-rank percentage substats, so they are safe to infer when the
		// label is unreadable.
		for _, target := range []float64{2.4, 7.2, 12.0} {
			if approxStatValue(value, target) {
				return "CRIT_RATE"
			}
		}
	}
	return ""
}

func inferMainStatTypeBySlotValue(slot int, value float64) string {
	if slot == 1 {
		return "HP_FLAT"
	}
	if slot == 2 {
		return "ATK_FLAT"
	}
	if slot == 3 {
		return "DEF_FLAT"
	}
	allowed := ocrSlotMainAllowed[slot]
	best := ""
	bestDiff := math.Inf(1)
	for t := range allowed {
		if expected, ok := ocrDefaultMainStatValue(t); ok {
			diff := math.Abs(value - expected)
			if diff < bestDiff {
				bestDiff = diff
				best = t
			}
		}
	}
	if best != "" && bestDiff <= math.Max(2, math.Abs(value)*0.08) {
		return best
	}
	return ""
}

func ocrDefaultMainStatValue(statType string) (float64, bool) {
	switch statType {
	case "HP_FLAT":
		return 2200, true
	case "ATK_FLAT":
		return 316, true
	case "DEF_FLAT":
		return 184, true
	case "HP_PERCENT", "ATK_PERCENT", "FIRE_DMG", "ICE_DMG", "ELECTRIC_DMG", "PHYSICAL_DMG", "ETHER_DMG", "WIND_DMG", "LUMIFLUX_DMG", "ANOMALY_MASTERY":
		return 30, true
	case "DEF_PERCENT", "CRIT_DMG":
		return 48, true
	case "CRIT_RATE", "PEN_RATIO":
		return 24, true
	case "ANOMALY_PROFICIENCY":
		return 92, true
	case "ENERGY_REGEN":
		return 60, true
	case "IMPACT":
		return 18, true
	}
	return 0, false
}

func statTypeFromOCRLabel(label string, percent bool) string {
	label = normalizeOCRText(label)
	ascii := compactASCII(label)
	if strings.Contains(ascii, "CRITDMG") || strings.Contains(ascii, "CDMG") || strings.Contains(ascii, "DMG") || strings.Contains(ascii, "REGS") || strings.Contains(ascii, "RFGS") || strings.Contains(ascii, "BGS") {
		return "CRIT_DMG"
	}
	if strings.Contains(ascii, "CRITRATE") || strings.Contains(ascii, "CRITR") || strings.Contains(ascii, "RATE") || strings.Contains(ascii, "CR") {
		return "CRIT_RATE"
	}
	if strings.Contains(ascii, "CRIT") && !strings.Contains(ascii, "DMG") {
		return "CRIT_RATE"
	}
	if strings.Contains(ascii, "ATK") || strings.Contains(ascii, "ATTACK") {
		if percent {
			return "ATK_PERCENT"
		}
		return "ATK_FLAT"
	}
	if strings.Contains(ascii, "HP") {
		if percent {
			return "HP_PERCENT"
		}
		return "HP_FLAT"
	}
	if strings.Contains(ascii, "DEF") {
		if percent {
			return "DEF_PERCENT"
		}
		return "DEF_FLAT"
	}
	han := compactHan(label)
	if strings.Contains(han, "暴击伤害") || strings.Contains(han, "暴伤") || (strings.Contains(han, "暴") && (strings.Contains(han, "伤") || strings.Contains(han, "害"))) {
		return "CRIT_DMG"
	}
	if likelyCritRateOCRLabel(han) || (percent && strings.Contains(han, "暴击")) {
		return "CRIT_RATE"
	}
	if strings.Contains(han, "异常精通") {
		return "ANOMALY_PROFICIENCY"
	}
	if strings.Contains(han, "异常掌控") {
		return "ANOMALY_MASTERY"
	}
	if strings.Contains(han, "能量自动回复") || strings.Contains(han, "能量回复") {
		return "ENERGY_REGEN"
	}
	if strings.Contains(han, "火属性伤害") {
		return "FIRE_DMG"
	}
	if strings.Contains(han, "冰属性伤害") {
		return "ICE_DMG"
	}
	if strings.Contains(han, "电属性伤害") {
		return "ELECTRIC_DMG"
	}
	if strings.Contains(han, "物理属性伤害") || strings.Contains(han, "物理伤害") {
		return "PHYSICAL_DMG"
	}
	if strings.Contains(han, "以太属性伤害") || strings.Contains(han, "以太伤害") {
		return "ETHER_DMG"
	}
	if strings.Contains(han, "风属性伤害") || strings.Contains(han, "风伤害") {
		return "WIND_DMG"
	}
	if strings.Contains(han, "流明属性伤害") || strings.Contains(han, "流明伤害") {
		return "LUMIFLUX_DMG"
	}
	if strings.Contains(han, "穿透率") {
		return "PEN_RATIO"
	}
	if strings.Contains(han, "穿透值") || strings.Contains(han, "穿透") {
		if percent {
			return "PEN_RATIO"
		}
		return "PEN_FLAT"
	}
	if strings.Contains(han, "冲击力") {
		return "IMPACT"
	}
	if strings.Contains(han, "生命值") || han == "生命" || strings.Contains(han, "生命什") {
		if percent {
			return "HP_PERCENT"
		}
		return "HP_FLAT"
	}
	if strings.Contains(han, "攻击力") || han == "攻击" {
		if percent {
			return "ATK_PERCENT"
		}
		return "ATK_FLAT"
	}
	if strings.Contains(han, "防御力") || han == "防御" {
		if percent {
			return "DEF_PERCENT"
		}
		return "DEF_FLAT"
	}
	best := ""
	bestDist := 999
	for _, candidate := range ocrStatLabels {
		d := levenshteinRunes(han, candidate)
		if d < bestDist {
			bestDist = d
			best = candidate
		}
	}
	if best != "" && (bestDist <= 1 || (best == "暴击率" && bestDist <= 2) || (len([]rune(best)) >= 4 && bestDist <= 2)) {
		return statTypeFromOCRLabel(best, percent)
	}
	return ""
}

func likelyCritRateOCRLabel(han string) bool {
	han = strings.TrimSpace(han)
	if han == "" {
		return false
	}
	if strings.Contains(han, "暴击伤害") || strings.Contains(han, "暴伤") || strings.Contains(han, "伤") || strings.Contains(han, "害") {
		return false
	}
	if strings.Contains(han, "攻击") || strings.Contains(han, "生命") || strings.Contains(han, "防御") || strings.Contains(han, "穿透") || strings.Contains(han, "异常") || strings.Contains(han, "属性") || strings.Contains(han, "能量") || strings.Contains(han, "冲击") {
		return false
	}
	if strings.Contains(han, "暴击率") || strings.Contains(han, "爆击率") || strings.Contains(han, "暴率") || strings.Contains(han, "击率") || (strings.Contains(han, "暴") && strings.Contains(han, "率")) {
		return true
	}
	if strings.Contains(han, "暴击") {
		return true
	}
	// Common OCR substitutions for “率” in the ZZZ UI font.  These are only
	// accepted when the label still contains 暴 or 击, to avoid false matches.
	misreadRateChars := "辛幸宰车丰半卒事守卑牵享翠率皋牢军宀"
	if strings.Contains(han, "暴") || strings.Contains(han, "率") {
		for _, r := range misreadRateChars {
			if strings.ContainsRune(han, r) && levenshteinRunes(han, "暴击率") <= 2 {
				return true
			}
		}
	}
	return levenshteinRunes(han, "暴击率") <= 2 && (strings.Contains(han, "暴") || strings.Contains(han, "率"))
}

func compactASCII(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		if r >= 'A' && r <= 'Z' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func compactHan(s string) string {
	s = normalizeOCRText(s)
	var b strings.Builder
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func bestSetName(raw string) (string, bool) {
	raw = compactHan(raw)
	if raw == "" {
		return "", false
	}
	for _, name := range builtinSetNames {
		if raw == compactHan(name) || strings.Contains(raw, compactHan(name)) || strings.Contains(compactHan(name), raw) {
			return name, name != raw
		}
	}
	best := raw
	bestDist := 999
	bestLen := 1
	for _, name := range builtinSetNames {
		cn := compactHan(name)
		d := levenshteinRunes(raw, cn)
		if d < bestDist {
			bestDist = d
			best = name
			bestLen = maxInt(len([]rune(raw)), len([]rune(cn)))
		}
	}
	if bestDist <= 2 || float64(bestLen-bestDist)/float64(bestLen) >= 0.58 {
		return best, true
	}
	return raw, false
}

func levenshteinRunes(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	if len(ra) == 0 {
		return len(rb)
	}
	if len(rb) == 0 {
		return len(ra)
	}
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i, ca := range ra {
		cur[0] = i + 1
		for j, cb := range rb {
			cost := 0
			if ca != cb {
				cost = 1
			}
			cur[j+1] = minInt(minInt(cur[j]+1, prev[j+1]+1), prev[j]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func handleShutdown(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]string{"ok": "true"})
	go func() {
		time.Sleep(200 * time.Millisecond)
		os.Exit(0)
	}()
}

func addUniqueNote(notes []string, note string) []string {
	note = strings.TrimSpace(note)
	if note == "" {
		return notes
	}
	for _, existing := range notes {
		if existing == note {
			return notes
		}
	}
	return append(notes, note)
}

func updateDiscMainStat(d Disc, main StatValue) Disc {
	d.MainStat = main
	if len(d.Stats) > 0 {
		d.Stats[0] = main
	} else {
		d.Stats = []StatValue{main}
	}
	return d
}

func repairImpossibleFlatPercentSubStat(s StatValue) (StatValue, bool) {
	t := strings.TrimSpace(s.Type)
	v := s.Value
	if t == "HP_FLAT" && approxOneOf(v, []float64{3, 6, 9, 12, 15, 18}) {
		s.Type = "HP_PERCENT"
		return s, true
	}
	if t == "ATK_FLAT" && approxOneOf(v, []float64{3, 6, 9, 12, 15, 18}) {
		s.Type = "ATK_PERCENT"
		return s, true
	}
	if t == "DEF_FLAT" && approxOneOf(v, []float64{4.8, 9.6, 14.4, 19.2, 24}) {
		s.Type = "DEF_PERCENT"
		return s, true
	}
	return s, false
}

func approxOneOf(v float64, values []float64) bool {
	for _, target := range values {
		if math.Abs(v-target) <= 0.061 {
			return true
		}
	}
	return false
}

func hasSubStatType(d Disc, statType string) bool {
	for _, s := range discWordStats(d) {
		if strings.TrimSpace(s.Type) == statType {
			return true
		}
	}
	return false
}

func repairDiscForOptimize(d Disc, allowed map[string]bool) (Disc, []string) {
	notes := []string{}
	changedSub := false
	for i := range d.SubStats {
		if fixed, ok := repairImpossibleFlatPercentSubStat(d.SubStats[i]); ok {
			notes = addUniqueNote(notes, fmt.Sprintf("%d号位「%s」有副词条被记录为「%s %.1f」，已按百分比词条「%s %.1f%%」临时参与计算；建议回库存编辑确认。", d.Slot, canonicalSetName(d.SetName), statCNName(d.SubStats[i].Type), d.SubStats[i].Value, statCNName(fixed.Type), fixed.Value))
			d.SubStats[i] = fixed
			changedSub = true
		}
	}
	if changedSub && len(d.Stats) > 0 {
		stats := []StatValue{discMainStat(d)}
		stats = append(stats, d.SubStats...)
		d.Stats = stats
	}
	main := discMainStat(d)
	if len(allowed) > 0 && strings.TrimSpace(main.Type) != "" && !allowed[main.Type] {
		// OCR can confuse a 30% main stat label. A strong signal is: the stored main
		// stat is HP%/ATK%/DEF%, but after correcting an impossible flat sub-stat we
		// would have the same percentage sub-stat as the main stat, which the game
		// does not allow. If the current optimizer lock contains another 30% main stat
		// candidate, use that locked type for search instead of silently discarding the
		// disc. This fixes cases like 5号位 攻击力% being OCR-saved as 生命值% while
		// a sub-stat “生命值 3%” was also saved as HP_FLAT 3.
		if hasSubStatType(d, main.Type) {
			candidates := []string{}
			for t := range allowed {
				if expected, ok := ocrDefaultMainStatValue(t); ok && math.Abs(expected-main.Value) <= ocrMainStatValueTolerance(expected) {
					if slotAllowed := ocrSlotMainAllowed[d.Slot]; len(slotAllowed) == 0 || slotAllowed[t] {
						candidates = append(candidates, t)
					}
				}
			}
			sort.Strings(candidates)
			if len(candidates) == 1 {
				oldType := main.Type
				main.Type = candidates[0]
				main.Suspect = true
				d = updateDiscMainStat(d, main)
				notes = addUniqueNote(notes, fmt.Sprintf("%d号位「%s」主属性原记录为「%s %.1f」，但与副词条冲突且当前锁定主属性为「%s」；本次配装临时按「%s %.1f」参与计算，请回库存编辑确认。", d.Slot, canonicalSetName(d.SetName), statCNName(oldType), main.Value, statCNName(candidates[0]), statCNName(candidates[0]), main.Value))
			}
		}
	}
	return d, notes
}

func optimize(ctx context.Context, req OptimizeRequest) OptimizeResponse {
	resp := OptimizeResponse{
		TotalDiscs:      len(req.Discs),
		CandidateCounts: map[string]int{},
		Results:         []OptimizeResult{},
	}
	if err := ctx.Err(); err != nil {
		resp.Canceled = true
		resp.Message = "配装计算已强制终止。"
		return resp
	}
	if req.TopN <= 0 || req.TopN > 200 {
		req.TopN = 20
	}
	if req.TopKPerSlot <= 0 || req.TopKPerSlot > 5000 {
		req.TopKPerSlot = 80
	}
	if req.MaxCombinations <= 0 {
		req.MaxCombinations = 2000000
	}
	if req.WordCoef == 0 {
		req.WordCoef = 5
	}
	if req.OverflowPenalty == 0 {
		req.OverflowPenalty = 0.5
	}
	if req.WantedWeights == nil {
		req.WantedWeights = map[string]float64{}
	}
	if req.ExtraStats == nil {
		req.ExtraStats = map[string]float64{}
	}
	if req.CombatExtraStats == nil {
		req.CombatExtraStats = map[string]float64{}
	}
	req.SetPattern = strings.TrimSpace(req.SetPattern)
	req.Required4Set = canonicalSetName(req.Required4Set)
	req.Required2Set = canonicalSetName(req.Required2Set)
	if req.SetPattern == "4+2" {
		if req.Required4Set == "" || req.Required2Set == "" {
			resp.Message = "请先选择 4 件套和 2 件套。"
			return resp
		}
		if req.Required4Set == req.Required2Set {
			resp.Message = "4 件套和 2 件套不能选择同一个套装。"
			return resp
		}
	}
	// 用户界面目前没有自定义词条权重输入，所以服务端按角色职业强制采用
	// 角色加权排序口径，避免“最高有效词条”策略仍然过度偏向纯双暴。
	// 强攻：暴击率 / 暴击伤害 / 攻击力% 为核心有效词条；
	// 命破：暴击率 / 暴击伤害 / 生命值% 为核心有效词条。
	req.WantedWeights = roleEffectiveWeights(req.RoleSystem, req.Mode, req.WantedWeights)
	// 强攻/命破模式使用常见基础面板；异常模式会保留这些数值供结果展示，但不检查暴击率目标。
	if req.BaseCritRate == 0 {
		req.BaseCritRate = 5
	}
	if req.BaseCritDmg == 0 {
		req.BaseCritDmg = 50
	}

	candidates := map[int][]Disc{1: {}, 2: {}, 3: {}, 4: {}, 5: {}, 6: {}}
	allowedBySlot := normalizeAllowed(req.SlotAllowedMainStats)
	repairNotes := []string{}
	for _, d := range req.Discs {
		if d.Slot < 1 || d.Slot > 6 {
			continue
		}
		if req.ExcludeDiscarded && d.Discarded {
			continue
		}
		if req.SetPattern == "4+2" && req.Required4Set != "" && req.Required2Set != "" {
			setName := canonicalSetName(d.SetName)
			if setName != req.Required4Set && setName != req.Required2Set {
				continue
			}
		}
		if allowed, ok := allowedBySlot[d.Slot]; ok && len(allowed) > 0 {
			var notes []string
			d, notes = repairDiscForOptimize(d, allowed)
			for _, note := range notes {
				repairNotes = addUniqueNote(repairNotes, note)
			}
			mainStat := discMainStat(d)
			if !allowed[mainStat.Type] {
				continue
			}
		} else {
			var notes []string
			d, notes = repairDiscForOptimize(d, nil)
			for _, note := range notes {
				repairNotes = addUniqueNote(repairNotes, note)
			}
		}
		candidates[d.Slot] = append(candidates[d.Slot], d)
	}

	for slot := 1; slot <= 6; slot++ {
		sort.SliceStable(candidates[slot], func(i, j int) bool {
			return discRoughScore(candidates[slot][i], req.WantedWeights, req.Mode, req.TargetCritRate, req.Required4Set, req.Required2Set) > discRoughScore(candidates[slot][j], req.WantedWeights, req.Mode, req.TargetCritRate, req.Required4Set, req.Required2Set)
		})
	}

	// v19: prefer exact search over rough Top-K pruning. In earlier builds, a
	// high-value combination could be missed if one of its discs was outside the
	// per-slot rough-score cut, even though the filtered 4+2 search space was
	// actually small enough to enumerate. We now keep every candidate whenever
	// the full product is within the requested combination budget, and only apply
	// Top-K shrinking when the raw search is too large.
	if rawProd := productCandidateCounts(candidates); rawProd > req.MaxCombinations {
		for slot := 1; slot <= 6; slot++ {
			if len(candidates[slot]) > req.TopKPerSlot {
				candidates[slot] = append([]Disc{}, candidates[slot][:req.TopKPerSlot]...)
			}
		}
	}

	for slot := 1; slot <= 6; slot++ {
		resp.CandidateCounts[strconv.Itoa(slot)] = len(candidates[slot])
		if len(candidates[slot]) == 0 {
			mainLimit := ""
			if allowed, ok := allowedBySlot[slot]; ok && len(allowed) > 0 {
				mainLimit = fmt.Sprintf("，主属性限制为「%s」", allowedMainStatSummary(allowed))
			}
			if req.SetPattern == "4+2" {
				detail := slotAvailabilitySummary(req, slot)
				resp.Message = fmt.Sprintf("%d 号位没有可用候选盘。请检查所选 4 件套「%s」和 2 件套「%s」在该槽位是否有库存%s。%s", slot, req.Required4Set, req.Required2Set, mainLimit, detail)
			} else {
				resp.Message = fmt.Sprintf("%d 号位没有可用候选盘。请检查库存或主属性限制%s。", slot, mainLimit)
			}
			return resp
		}
	}

	// If the rough candidate pool is still too large, shrink the largest slot pool until the search is bounded.
	prod := productCandidateCounts(candidates)
	for prod > req.MaxCombinations {
		largestSlot := 1
		for slot := 2; slot <= 6; slot++ {
			if len(candidates[slot]) > len(candidates[largestSlot]) {
				largestSlot = slot
			}
		}
		if len(candidates[largestSlot]) <= 1 {
			break
		}
		candidates[largestSlot] = candidates[largestSlot][:len(candidates[largestSlot])-1]
		prod = productCandidateCounts(candidates)
	}
	resp.SearchedCombinations = prod
	for slot := 1; slot <= 6; slot++ {
		resp.CandidateCounts[strconv.Itoa(slot)] = len(candidates[slot])
	}

	// Static 2-piece set effects are added during build evaluation. Conditional
	// 4-piece effects are not counted by default because they do not appear in the
	// out-of-combat details panel and have rotation-dependent uptime.
	effectMap := map[string]SetEffect{}

	results := make([]OptimizeResult, 0, req.TopN*8)
	nearMisses := make([]OptimizeResult, 0, 8)
	build := make([]Disc, 0, 6)
	exactSetMode := req.SetPattern == "4+2" && req.Required4Set != "" && req.Required2Set != ""
	var searched int64
	canceled := false
	var eval func(slot int, count4 int, count2 int)
	eval = func(slot int, count4 int, count2 int) {
		if canceled {
			return
		}
		select {
		case <-ctx.Done():
			canceled = true
			return
		default:
		}
		if exactSetMode {
			remaining := 7 - slot
			if count4 > 4 || count2 > 2 || count4+remaining < 4 || count2+remaining < 2 {
				return
			}
		}
		if slot == 7 {
			if exactSetMode && (count4 != 4 || count2 != 2) {
				return
			}
			searched++
			res, ok := evaluateBuild(build, req, effectMap)
			if !ok {
				if !isAnomalyMode(req.Mode) {
					debugReq := req
					debugReq.TargetCritRate = -9999
					debugReq.MinPanelCritDmg = 0
					debugReq.MinFinalAttack = 0
					debugReq.MinSheerForce = 0
					if miss, missOK := evaluateBuild(build, debugReq, effectMap); missOK {
						miss.Reason = thresholdDiagnosticText(miss, req)
						nearMisses = appendNearMiss(nearMisses, miss, 5)
					}
				}
				return
			}
			results = append(results, res)
			if len(results) > req.TopN*16 {
				sortResults(results, req.Mode)
				results = results[:req.TopN*4]
			}
			return
		}
		for _, d := range candidates[slot] {
			if canceled {
				return
			}
			select {
			case <-ctx.Done():
				canceled = true
				return
			default:
			}
			next4, next2 := count4, count2
			if exactSetMode {
				setName := canonicalSetName(d.SetName)
				if setName == req.Required4Set {
					next4++
				} else if setName == req.Required2Set {
					next2++
				} else {
					continue
				}
			}
			build = append(build, d)
			eval(slot+1, next4, next2)
			build = build[:len(build)-1]
		}
	}
	eval(1, 0, 0)
	resp.SearchedCombinations = searched
	if canceled || ctx.Err() != nil {
		resp.Canceled = true
		resp.Message = fmt.Sprintf("配装计算已强制终止：已搜索 %d 套组合，结果未完成。", searched)
		if len(repairNotes) > 0 {
			resp.Message += " 临时修正提示：" + strings.Join(repairNotes, "；")
		}
		return resp
	}

	if len(results) == 0 {
		if len(nearMisses) > 0 {
			for i := range nearMisses {
				nearMisses[i].Rank = i + 1
			}
			resp.NearMisses = nearMisses
		}
		if req.SetPattern == "4+2" {
			if len(nearMisses) > 0 {
				best := nearMisses[0]
				resp.Message = fmt.Sprintf("没有找到同时满足 4+2、主属性限制、角色界面暴击率目标和阈值要求的方案（已允许目标上下各 1 个暴击率词条，即 ±2.4%%）：4 件套「%s」+ 2 件套「%s」。最接近候选面板暴击率 %.1f%%，目标 %.1f%%。", req.Required4Set, req.Required2Set, best.PanelCritRate, req.TargetCritRate)
			} else {
				resp.Message = fmt.Sprintf("没有找到同时满足 4+2、主属性限制、角色界面暴击率目标和阈值要求的方案（已允许目标上下各 1 个暴击率词条，即 ±2.4%%）：4 件套「%s」+ 2 件套「%s」。请检查库存数量、主属性限制，或降低期望暴击率/阈值。", req.Required4Set, req.Required2Set)
			}
		} else if isAnomalyMode(req.Mode) {
			resp.Message = "没有找到方案。请检查每个槽位是否都有可用驱动盘，或提高每槽候选数。"
		} else {
			if len(nearMisses) > 0 {
				best := nearMisses[0]
				resp.Message = fmt.Sprintf("没有找到满足面板暴击率目标和阈值要求的方案（已允许目标上下各 1 个暴击率词条，即 ±2.4%%）。最接近候选面板暴击率 %.1f%%，目标 %.1f%%。", best.PanelCritRate, req.TargetCritRate)
			} else {
				resp.Message = "没有找到满足面板暴击率目标和阈值要求的方案（已允许目标上下各 1 个暴击率词条，即 ±2.4%）。可以降低期望暴击率/阈值，或提高每槽候选数。"
			}
		}
		if len(repairNotes) > 0 {
			resp.Message += " 临时修正提示：" + strings.Join(repairNotes, "；")
		}
		return resp
	}
	sortResults(results, req.Mode)
	if len(results) > req.TopN {
		results = results[:req.TopN]
	}
	for i := range results {
		results[i].Rank = i + 1
	}
	resp.Results = results
	resp.Message = fmt.Sprintf("完成：搜索了 %d 套组合，返回前 %d 套。", searched, len(results))
	if len(repairNotes) > 0 {
		resp.Message += " 临时修正提示：" + strings.Join(repairNotes, "；")
	}
	return resp
}

func allowedMainStatSummary(allowed map[string]bool) string {
	keys := make([]string, 0, len(allowed))
	for k := range allowed {
		if strings.TrimSpace(k) != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	labels := make([]string, 0, len(keys))
	for _, k := range keys {
		labels = append(labels, statCNName(k))
	}
	return strings.Join(labels, " / ")
}

func slotAvailabilitySummary(req OptimizeRequest, slot int) string {
	sets := []string{}
	if req.Required4Set != "" {
		sets = append(sets, req.Required4Set)
	}
	if req.Required2Set != "" && req.Required2Set != req.Required4Set {
		sets = append(sets, req.Required2Set)
	}
	parts := []string{}
	if len(sets) > 0 {
		setParts := make([]string, 0, len(sets))
		for _, setName := range sets {
			counts := map[string]int{}
			for _, d := range req.Discs {
				if d.Slot != slot {
					continue
				}
				if req.ExcludeDiscarded && d.Discarded {
					continue
				}
				if canonicalSetName(d.SetName) != canonicalSetName(setName) {
					continue
				}
				main := discMainStat(d)
				if strings.TrimSpace(main.Type) != "" {
					counts[main.Type]++
				}
			}
			setParts = append(setParts, fmt.Sprintf("%s：%s", setName, statCountSummaryByType(counts)))
		}
		parts = append(parts, "所选套装内："+strings.Join(setParts, "；"))
	}
	all := map[string]int{}
	for _, d := range req.Discs {
		if d.Slot != slot {
			continue
		}
		if req.ExcludeDiscarded && d.Discarded {
			continue
		}
		main := discMainStat(d)
		if strings.TrimSpace(main.Type) != "" {
			all[main.Type]++
		}
	}
	if len(all) > 0 {
		parts = append(parts, "全库存该槽位："+statCountSummaryByType(all))
	}
	if len(parts) == 0 {
		return ""
	}
	return " 当前库存情况：" + strings.Join(parts, "。") + "。"
}

func statCountSummaryByType(counts map[string]int) string {
	if len(counts) == 0 {
		return "无"
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.SliceStable(keys, func(i, j int) bool {
		return statCNName(keys[i]) < statCNName(keys[j])
	})
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s×%d", statCNName(k), counts[k]))
	}
	return strings.Join(parts, " / ")
}

func normalizeAllowed(src map[string][]string) map[int]map[string]bool {
	out := map[int]map[string]bool{}
	for k, arr := range src {
		slot, err := strconv.Atoi(k)
		if err != nil || slot < 1 || slot > 6 {
			continue
		}
		m := map[string]bool{}
		for _, t := range arr {
			t = strings.TrimSpace(t)
			if t != "" {
				m[t] = true
			}
		}
		out[slot] = m
	}
	return out
}

func productCandidateCounts(c map[int][]Disc) int64 {
	prod := int64(1)
	for slot := 1; slot <= 6; slot++ {
		prod *= int64(len(c[slot]))
		if prod < 0 || prod > math.MaxInt64/2 {
			return math.MaxInt64
		}
	}
	return prod
}

func roleEffectiveWeights(roleSystem string, mode string, current map[string]float64) map[string]float64 {
	role := strings.ToUpper(strings.TrimSpace(roleSystem))
	mode = strings.ToUpper(strings.TrimSpace(mode))
	if isAnomalyMode(mode) || role == "ANOMALY" {
		return map[string]float64{
			"ANOMALY_PROFICIENCY": 1,
			"ATK_PERCENT":         1,
			"ATK_FLAT":            1,
		}
	}
	if role == "RUPTURE" {
		return map[string]float64{
			"CRIT_RATE":   1,
			"CRIT_DMG":    1,
			"HP_PERCENT":  1,
			"HP_FLAT":     0.35,
			"ATK_PERCENT": 0.35,
			"ATK_FLAT":    0.15,
		}
	}
	if role == "STUN" {
		return map[string]float64{
			"CRIT_RATE": 0.65, "CRIT_DMG": 0.45,
			"IMPACT": 1, "ENERGY_REGEN": 0.7,
			"ATK_PERCENT": 0.75, "HP_PERCENT": 0.45,
			"ATK_FLAT": 0.2, "HP_FLAT": 0.15,
		}
	}
	if role == "DEFENSE" {
		return map[string]float64{
			"DEF_PERCENT": 1, "DEF_FLAT": 0.35,
			"HP_PERCENT": 0.8, "HP_FLAT": 0.2,
			"ATK_PERCENT": 0.75, "ATK_FLAT": 0.2,
			"IMPACT": 0.8, "ENERGY_REGEN": 0.7,
			"ANOMALY_PROFICIENCY": 0.35,
		}
	}
	if role == "SUPPORT" {
		return map[string]float64{
			"ENERGY_REGEN": 1, "ATK_PERCENT": 0.9,
			"HP_PERCENT": 0.7, "CRIT_RATE": 0.55,
			"CRIT_DMG": 0.45, "ANOMALY_PROFICIENCY": 0.45,
			"ATK_FLAT": 0.2, "HP_FLAT": 0.15,
		}
	}
	return map[string]float64{
		"CRIT_RATE":   1,
		"CRIT_DMG":    1,
		"ATK_PERCENT": 1,
		"ATK_FLAT":    0.35,
	}
}

func thresholdDiagnosticText(res OptimizeResult, req OptimizeRequest) string {
	parts := []string{}
	if req.TargetCritRate > 0 && (res.PanelCritRate+critTargetTolerance+1e-9 < req.TargetCritRate || res.PanelCritRate-critTargetTolerance-1e-9 > req.TargetCritRate) {
		parts = append(parts, fmt.Sprintf("面板暴击率 %.1f%% / 期望 %.1f%%，超出 ±1 个暴击率词条窗口", res.PanelCritRate, req.TargetCritRate))
	}
	if targetCritDmg := effectiveTargetCritDmg(req); targetCritDmg > 0 && res.PanelCritDmg+1e-9 < targetCritDmg {
		parts = append(parts, fmt.Sprintf("面板暴击伤害 %.1f%% / 期望 %.1f%%，差 %.1f%%", res.PanelCritDmg, targetCritDmg, math.Max(0, targetCritDmg-res.PanelCritDmg)))
	}
	if req.TargetFinalAttack > 0 {
		if roleIsUtility(req.RoleSystem) || strings.EqualFold(strings.TrimSpace(req.Mode), "UTILITY_BALANCE") {
			tol := utilityTargetTolerance(req.TargetFinalAttack)
			if res.FinalAttack+tol+1e-9 < req.TargetFinalAttack || res.FinalAttack-tol-1e-9 > req.TargetFinalAttack {
				parts = append(parts, fmt.Sprintf("总攻击 %.0f / 期望 %.0f，超出目标窗口 %.0f", res.FinalAttack, req.TargetFinalAttack, tol))
			}
		} else if res.FinalAttack+1e-9 < req.TargetFinalAttack {
			parts = append(parts, fmt.Sprintf("总攻击 %.0f / 期望 %.0f，差 %.0f", res.FinalAttack, req.TargetFinalAttack, math.Max(0, req.TargetFinalAttack-res.FinalAttack)))
		}
	}
	if req.TargetFinalHP > 0 {
		if roleIsUtility(req.RoleSystem) || strings.EqualFold(strings.TrimSpace(req.Mode), "UTILITY_BALANCE") {
			tol := utilityTargetTolerance(req.TargetFinalHP)
			if res.FinalHP+tol+1e-9 < req.TargetFinalHP || res.FinalHP-tol-1e-9 > req.TargetFinalHP {
				parts = append(parts, fmt.Sprintf("生命值 %.0f / 期望 %.0f，超出目标窗口 %.0f", res.FinalHP, req.TargetFinalHP, tol))
			}
		} else if res.FinalHP+1e-9 < req.TargetFinalHP {
			parts = append(parts, fmt.Sprintf("生命值 %.0f / 期望 %.0f，差 %.0f", res.FinalHP, req.TargetFinalHP, math.Max(0, req.TargetFinalHP-res.FinalHP)))
		}
	}
	if req.TargetFinalDefense > 0 {
		if roleIsUtility(req.RoleSystem) || strings.EqualFold(strings.TrimSpace(req.Mode), "UTILITY_BALANCE") {
			tol := utilityTargetTolerance(req.TargetFinalDefense)
			if res.FinalDefense+tol+1e-9 < req.TargetFinalDefense || res.FinalDefense-tol-1e-9 > req.TargetFinalDefense {
				parts = append(parts, fmt.Sprintf("防御力 %.0f / 期望 %.0f，超出目标窗口 %.0f", res.FinalDefense, req.TargetFinalDefense, tol))
			}
		} else if res.FinalDefense+1e-9 < req.TargetFinalDefense {
			parts = append(parts, fmt.Sprintf("防御力 %.0f / 期望 %.0f，差 %.0f", res.FinalDefense, req.TargetFinalDefense, math.Max(0, req.TargetFinalDefense-res.FinalDefense)))
		}
	}
	// User-entered AP goals refer to the value shown on the in-game agent panel.
	// Triggered W-Engine and 4-piece bonuses live in CombatStats and must not make
	// a build pass a panel-facing target.
	ap := res.Stats["ANOMALY_PROFICIENCY"]
	if req.TargetAnomalyProficiency > 0 && ap+anomalyTargetTolerance+1e-9 < req.TargetAnomalyProficiency {
		parts = append(parts, fmt.Sprintf("异常精通 %.0f / 期望 %.0f，低于期望超过 %.0f", ap, req.TargetAnomalyProficiency, anomalyTargetTolerance))
	}
	if roleIsRupture(req.RoleSystem) && req.MinSheerForce > 0 && res.SheerForce+1e-9 < req.MinSheerForce {
		parts = append(parts, fmt.Sprintf("贯穿力 %.0f / 阈值 %.0f，差 %.0f", res.SheerForce, req.MinSheerForce, math.Max(0, req.MinSheerForce-res.SheerForce)))
	}
	if len(parts) == 0 {
		parts = append(parts, "该候选满足 4+2 和主属性限制，但被其他筛选条件过滤")
	}
	return "未达标诊断：" + strings.Join(parts, "；") + "。"
}

func discRoughScore(d Disc, weights map[string]float64, mode string, targetCR float64, req4 string, req2 string) float64 {
	_ = targetCR
	modeUpper := strings.ToUpper(strings.TrimSpace(mode))
	wordMode := modeUpper == "MAX_WORDS" || modeUpper == "ANOMALY_WORDS"
	score := 0.0
	for _, s := range discWordStats(d) {
		words := statWords(s)
		if words > 0 {
			if len(weights) == 0 {
				score += words * 8
			} else if w := weights[s.Type]; w > 0 {
				if wordMode {
					score += words * w * 30
				} else {
					score += words * w * 12
				}
			}
		}
	}
	for _, s := range discAllStats(d) {
		switch s.Type {
		case "CRIT_RATE":
			if !isAnomalyMode(mode) {
				if wordMode {
					score += s.Value * 0.6
				} else {
					score += s.Value * 2.3
				}
			} else {
				score += s.Value * 0.1
			}
		case "CRIT_DMG":
			if !isAnomalyMode(mode) {
				if wordMode {
					score += s.Value * 0.25
				} else {
					score += s.Value * 1.1
				}
			} else {
				score += s.Value * 0.05
			}
		case "ATK_PERCENT":
			if isAnomalyMode(mode) {
				score += s.Value * 1.2
			} else {
				score += s.Value * 0.55
			}
		case "HP_PERCENT":
			score += s.Value * 0.4
		case "PEN_FLAT":
			score += s.Value * 0.08
		case "ANOMALY_PROFICIENCY":
			if isAnomalyMode(mode) {
				score += s.Value * 0.8
			} else {
				score += s.Value * 0.08
			}
		}
	}
	setName := canonicalSetName(d.SetName)
	if strings.TrimSpace(req4) != "" && setName == canonicalSetName(req4) {
		score += 25
	}
	if strings.TrimSpace(req2) != "" && setName == canonicalSetName(req2) {
		score += 10
	}
	return score
}

func roleIsAttack(role string) bool {
	role = strings.ToUpper(strings.TrimSpace(role))
	return role == "ATTACK" || role == "STRONG"
}

func roleIsRupture(role string) bool {
	return strings.ToUpper(strings.TrimSpace(role)) == "RUPTURE"
}

func roleIsSupport(role string) bool {
	return strings.ToUpper(strings.TrimSpace(role)) == "SUPPORT"
}

func roleIsStun(role string) bool {
	return strings.ToUpper(strings.TrimSpace(role)) == "STUN"
}

func roleIsUtility(role string) bool {
	role = strings.ToUpper(strings.TrimSpace(role))
	return role == "SUPPORT" || role == "STUN" || role == "DEFENSE"
}

func utilityTargetTolerance(target float64) float64 {
	if target <= 0 {
		return 0
	}
	return math.Max(1, target*utilityTargetRelativeTolerance)
}

func effectiveTargetCritDmg(req OptimizeRequest) float64 {
	if req.TargetCritDmg > 0 {
		return req.TargetCritDmg
	}
	return req.MinPanelCritDmg
}

func effectiveTargetFinalAttack(req OptimizeRequest) float64 {
	if req.TargetFinalAttack > 0 {
		return req.TargetFinalAttack
	}
	return req.MinFinalAttack
}

func isStrictTargetMode(mode string) bool {
	mode = strings.ToUpper(strings.TrimSpace(mode))
	return mode == "STRICT_TARGETS" || mode == "STRICT_TARGET"
}

func normalizedStrictGap(actual, target, unit float64) float64 {
	if target <= 0 {
		return 0
	}
	if unit <= 0 {
		unit = math.Max(1, math.Abs(target)*0.01)
	}
	return math.Abs(actual-target) / unit
}

func strictTargetPenalty(resStats map[string]float64, panelCritRate float64, panelCritDmg float64, finalATK float64, finalHP float64, finalDEF float64, ap float64, req OptimizeRequest) (float64, []string) {
	baseATKForRoll := req.BaseATK + resStats["BASE_ATK"]
	if baseATKForRoll <= 0 {
		baseATKForRoll = math.Max(1, finalATK)
	}
	baseHPForRoll := req.BaseHP + resStats["BASE_HP"]
	if baseHPForRoll <= 0 {
		baseHPForRoll = math.Max(1, finalHP)
	}
	baseDEFForRoll := req.BaseDEF + resStats["BASE_DEF"]
	if baseDEFForRoll <= 0 {
		baseDEFForRoll = math.Max(1, finalDEF)
	}
	items := []struct {
		label  string
		actual float64
		target float64
		unit   float64
		weight float64
	}{
		{"暴击率", panelCritRate, req.TargetCritRate, 2.4, 100000000},
		{"暴击伤害", panelCritDmg, effectiveTargetCritDmg(req), 4.8, 1000000},
		{"攻击力", finalATK, effectiveTargetFinalAttack(req), math.Max(19, baseATKForRoll*0.03), 10000},
		{"生命值", finalHP, req.TargetFinalHP, math.Max(112, baseHPForRoll*0.03), 100},
		{"防御力", finalDEF, req.TargetFinalDefense, math.Max(15, baseDEFForRoll*0.048), 10},
		{"异常精通", ap, req.TargetAnomalyProficiency, 9, 1},
	}
	penalty := 0.0
	parts := []string{}
	for _, it := range items {
		if it.target <= 0 {
			continue
		}
		gap := normalizedStrictGap(it.actual, it.target, it.unit)
		penalty += gap * it.weight
		parts = append(parts, fmt.Sprintf("%s %.1f/%.1f（约差 %.2f 词条）", it.label, it.actual, it.target, gap))
	}
	return penalty, parts
}

func targetWindowPenalty(actual, target, tolerance float64) (shortfall float64, overflow float64, penalty float64, ok bool) {
	if target <= 0 {
		return 0, 0, 0, true
	}
	shortfall = math.Max(0, target-actual)
	overflow = math.Max(0, actual-target)
	if shortfall > tolerance+1e-9 || overflow > tolerance+1e-9 {
		return shortfall, overflow, 1, false
	}
	dist := math.Max(shortfall, overflow)
	if tolerance > 0 {
		penalty = math.Pow(dist/tolerance, 2) * 0.10
	}
	return shortfall, overflow, penalty, true
}

func critTargetPenalty(panelCritRate float64, targetCritRate float64) (shortfall float64, overflow float64, penalty float64) {
	if targetCritRate <= 0 {
		return 0, 0, 0
	}
	shortfall = math.Max(0, targetCritRate-panelCritRate)
	overflow = math.Max(0, panelCritRate-targetCritRate)
	// Inside the ±1-roll window, prefer builds closer to the target. Being over
	// target does not add value because scoreCritRate is capped later, but it is no
	// longer rejected unless it exceeds one roll.
	distanceRolls := math.Max(shortfall, overflow) / critTargetTolerance
	penalty = distanceRolls * distanceRolls * 0.10
	return shortfall, overflow, penalty
}

func normalizedAgentName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "·", "")
	name = strings.ReplaceAll(name, " ", "")
	name = strings.ReplaceAll(name, "星间雅", "星见雅")
	return name
}

func agentNameContains(name string, parts ...string) bool {
	name = normalizedAgentName(name)
	for _, part := range parts {
		if strings.Contains(name, normalizedAgentName(part)) {
			return true
		}
	}
	return false
}

func boolStatSet(keys ...string) map[string]bool {
	m := map[string]bool{}
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key != "" {
			m[key] = true
		}
	}
	return m
}

func gameEffectiveStatSet(req OptimizeRequest) map[string]bool {
	// v1.18：继续按 Pro v1.16 校准结论，将游戏内“有效副词条数量”
	// 按角色推荐有效属性逐档计数，
	// 与配装器内部用于排序的加权词条不同。这里保留排序权重不变，
	// 但新增一个按游戏面板口径显示的无权重有效词条数。
	name := normalizedAgentName(req.CharacterName)
	if agentNameContains(name, "南宫羽", "爱芮", "维琳娜") {
		return boolStatSet("ANOMALY_PROFICIENCY", "ATK_PERCENT")
	}
	if agentNameContains(name, "仪玄") {
		return boolStatSet("CRIT_RATE", "CRIT_DMG", "HP_PERCENT")
	}
	role := strings.ToUpper(strings.TrimSpace(req.RoleSystem))
	mode := strings.ToUpper(strings.TrimSpace(req.Mode))
	if isAnomalyMode(mode) || role == "ANOMALY" {
		return boolStatSet("ANOMALY_PROFICIENCY", "ATK_PERCENT", "ATK_FLAT")
	}
	if role == "RUPTURE" {
		return boolStatSet("CRIT_RATE", "CRIT_DMG", "HP_PERCENT")
	}
	if role == "ATTACK" || role == "STRONG" {
		return boolStatSet("CRIT_RATE", "CRIT_DMG", "ATK_PERCENT")
	}
	if role == "STUN" {
		return boolStatSet("CRIT_RATE", "CRIT_DMG", "ATK_PERCENT")
	}
	if role == "DEFENSE" {
		if agentNameContains(name, "本") {
			return boolStatSet("DEF_PERCENT", "HP_PERCENT")
		}
		if agentNameContains(name, "凯撒") {
			return boolStatSet("IMPACT", "HP_PERCENT", "ATK_PERCENT")
		}
		if agentNameContains(name, "赛斯", "潘引壶") {
			return boolStatSet("ATK_PERCENT", "ENERGY_REGEN", "HP_PERCENT")
		}
		return boolStatSet("DEF_PERCENT", "HP_PERCENT", "ATK_PERCENT")
	}
	if role == "SUPPORT" {
		return boolStatSet("ATK_PERCENT", "HP_PERCENT", "CRIT_RATE", "CRIT_DMG", "ANOMALY_PROFICIENCY")
	}
	return boolStatSet("CRIT_RATE", "CRIT_DMG", "ATK_PERCENT")
}

func evaluateBuild(build []Disc, req OptimizeRequest, effects map[string]SetEffect) (OptimizeResult, bool) {
	stats := map[string]float64{}
	for statType, value := range req.ExtraStats {
		statType = strings.TrimSpace(statType)
		if statType != "" && math.Abs(value) > 1e-9 {
			stats[statType] += value
		}
	}
	setCounts := map[string]int{}
	effWords := 0.0
	weightedWords := 0.0
	gameEffectiveWords := 0.0
	gameEffectiveStats := gameEffectiveStatSet(req)

	_ = effects // v3 does not calculate drive-disc set effects.
	for _, d := range build {
		for _, s := range discAllStats(d) {
			addStat(stats, s)
		}
		for _, s := range discWordStats(d) {
			if words := statWords(s); words > 0 {
				effWords += words
				if gameEffectiveStats[s.Type] {
					gameEffectiveWords += words
				}
				if len(req.WantedWeights) == 0 {
					weightedWords += words
				} else if req.WantedWeights[s.Type] > 0 {
					weightedWords += words * req.WantedWeights[s.Type]
				}
			}
		}
		setName := canonicalSetName(d.SetName)
		if setName != "" {
			setCounts[setName]++
		}
	}

	if !matchesSetConstraints(setCounts, req.SetPattern, req.Required4Set, req.Required2Set) {
		return OptimizeResult{}, false
	}

	applyTwoPiecePanelBonuses(stats, setCounts)
	applyConditionalFourPiecePanelBonuses(stats, setCounts, req.CharacterElement)

	panelCritRate := req.BaseCritRate + req.ExtraCritRate + stats["CRIT_RATE"]
	panelCritDmg := req.BaseCritDmg + req.ExtraCritDmg + stats["CRIT_DMG"]
	combatStats := cloneStatMap(stats)
	for statType, value := range req.CombatExtraStats {
		statType = strings.TrimSpace(statType)
		if statType != "" && math.Abs(value) > 1e-9 {
			combatStats[statType] += value
		}
	}
	applyFourPieceCombatBonuses(combatStats, setCounts)
	applyConditionalFourPieceCombatBonuses(combatStats, setCounts, req.CharacterElement)
	initialEnergyRegen, finalAnomalyMastery := applyCharacterCombatBonuses(combatStats, stats, req)
	critRate := req.BaseCritRate + req.ExtraCritRate + combatStats["CRIT_RATE"]
	critDmg := req.BaseCritDmg + req.ExtraCritDmg + combatStats["CRIT_DMG"]
	finalATK := calcFinalAttack(req.BaseATK, stats["BASE_ATK"], stats["ATK_PERCENT"], stats["ATK_FLAT"])
	finalHP := calcFinalHP(req.BaseHP, stats["BASE_HP"], stats["HP_PERCENT"], stats["HP_FLAT"])
	finalDEF := calcFinalDefense(req.BaseDEF, stats["BASE_DEF"], stats["DEF_PERCENT"], stats["DEF_FLAT"])
	combatFinalATK := calcFinalAttack(req.BaseATK, combatStats["BASE_ATK"], combatStats["ATK_PERCENT"], combatStats["ATK_FLAT"])
	combatFinalHP := calcFinalHP(req.BaseHP, combatStats["BASE_HP"], combatStats["HP_PERCENT"], combatStats["HP_FLAT"])
	combatFinalDEF := calcFinalDefense(req.BaseDEF, combatStats["BASE_DEF"], combatStats["DEF_PERCENT"], combatStats["DEF_FLAT"])
	sheerForce := 0.0
	if strings.EqualFold(strings.TrimSpace(req.RoleSystem), "RUPTURE") || strings.EqualFold(strings.TrimSpace(req.Mode), "RUPTURE_SHEER") {
		hpToSheerRatio := req.HPToSheerRatio
		if hpToSheerRatio == 0 {
			hpToSheerRatio = 0.1
		}
		sheerForce = math.Floor(combatFinalATK*0.3+1e-9) + math.Floor(combatFinalHP*hpToSheerRatio+1e-9) + combatStats["SHEER_FORCE"] + combatStats["SHEER_FORCE_FLAT"]
	}
	critMultiplier := calcCritMultiplier(critRate, critDmg)
	roleMetricForDamage := combatFinalATK
	if roleIsRupture(req.RoleSystem) {
		roleMetricForDamage = sheerForce
	}
	damageBonus := combatDamageBonusPercent(combatStats, req.CharacterElement)
	damageIndex := roleMetricForDamage * critMultiplier * (1 + damageBonus/100)
	if roleIsRupture(req.RoleSystem) {
		damageIndex *= 1 + combatStats["SHEER_DMG_BONUS"]/100
	}
	// For ranking only, treat the user target as the useful panel crit cap. Extra
	// panel crit above the target remains visible in the result, but it should not
	// increase the score because those rolls are regarded as wasted for the plan.
	scoreCritRate := critRate
	if req.TargetCritRate > 0 && panelCritRate > req.TargetCritRate {
		scoreCritRate = critRate - (panelCritRate - req.TargetCritRate)
	}
	scoreCritMultiplier := calcCritMultiplier(scoreCritRate, critDmg)
	scoreDamageIndex := roleMetricForDamage * scoreCritMultiplier * (1 + damageBonus/100)
	if roleIsRupture(req.RoleSystem) {
		scoreDamageIndex *= 1 + combatStats["SHEER_DMG_BONUS"]/100
	}

	// The user-entered target crit rate is intended to match the in-game
	// agent-details panel. W-Engine skill effects and conditional set effects are
	// kept in critRate / critDmg as combat reference values, but they must not be
	// used to satisfy the target crit-rate threshold.
	// Panel-facing anomaly modes must use the same values the player sees in the
	// agent-details screen. CombatStats intentionally keeps triggered bonuses for
	// the separate output reference, but it is not a substitute for panel stats.
	panelAP := stats["ANOMALY_PROFICIENCY"]
	overflow := 0.0
	utilityPlan := roleIsUtility(req.RoleSystem) || strings.EqualFold(strings.TrimSpace(req.Mode), "UTILITY_BALANCE")
	strictPlan := isStrictTargetMode(req.Mode)
	utilityPenalty := 0.0

	// v1.20: all roles share the same six visible target fields. A value of 0
	// means the field is not used. In normal strategies, CRIT Rate remains a
	// ±1-roll target window; Anomaly Proficiency is a soft lower bound; Crit DMG is
	// a lower bound; HP/ATK are lower bounds for damage roles and target windows for
	// Support/Stun/Defense utility planning. In strict target mode, these fields are not
	// hard filters: every non-zero target becomes a high-priority closeness score.
	if !strictPlan {
		if req.TargetCritRate > 0 {
			short, over, p, ok := targetWindowPenalty(panelCritRate, req.TargetCritRate, critTargetTolerance)
			if !ok {
				return OptimizeResult{}, false
			}
			utilityPenalty += p
			overflow = over
			_ = short
		}
		targetCritDmg := effectiveTargetCritDmg(req)
		if targetCritDmg > 0 && panelCritDmg+1e-9 < targetCritDmg {
			return OptimizeResult{}, false
		}
		if req.TargetAnomalyProficiency > 0 && panelAP+anomalyTargetTolerance+1e-9 < req.TargetAnomalyProficiency {
			return OptimizeResult{}, false
		}
		if req.TargetFinalHP > 0 {
			if utilityPlan {
				_, _, p, ok := targetWindowPenalty(finalHP, req.TargetFinalHP, utilityTargetTolerance(req.TargetFinalHP))
				if !ok {
					return OptimizeResult{}, false
				}
				utilityPenalty += p
			} else if finalHP+1e-9 < req.TargetFinalHP {
				return OptimizeResult{}, false
			}
		}
		if req.TargetFinalDefense > 0 {
			if utilityPlan {
				_, _, p, ok := targetWindowPenalty(finalDEF, req.TargetFinalDefense, utilityTargetTolerance(req.TargetFinalDefense))
				if !ok {
					return OptimizeResult{}, false
				}
				utilityPenalty += p
			} else if finalDEF+1e-9 < req.TargetFinalDefense {
				return OptimizeResult{}, false
			}
		}
		targetFinalAttack := effectiveTargetFinalAttack(req)
		if targetFinalAttack > 0 {
			if utilityPlan {
				_, _, p, ok := targetWindowPenalty(finalATK, targetFinalAttack, utilityTargetTolerance(targetFinalAttack))
				if !ok {
					return OptimizeResult{}, false
				}
				utilityPenalty += p
			} else if finalATK+1e-9 < targetFinalAttack {
				return OptimizeResult{}, false
			}
		}
		if roleIsRupture(req.RoleSystem) && req.MinSheerForce > 0 && sheerForce+1e-9 < req.MinSheerForce {
			return OptimizeResult{}, false
		}
	}
	// Ranking for crit-oriented modes follows a build-quality score instead of a
	// single raw stat. Crit rate may be up to one CRIT Rate roll below the panel
	// target, but both missing crit and wasted over-target crit are penalized.
	// Combat crit/damage bonuses are used for the output index, while the
	// user-entered target remains a panel-facing requirement.
	scoreCritDmg := panelCritDmg
	critShortfall, critOver, critPenalty := critTargetPenalty(panelCritRate, req.TargetCritRate)
	critFitFactor := math.Max(0, 1-critPenalty)
	mode := strings.ToUpper(strings.TrimSpace(req.Mode))
	strictPenalty := 0.0
	strictParts := []string{}
	if strictPlan {
		strictPenalty, strictParts = strictTargetPenalty(stats, panelCritRate, panelCritDmg, finalATK, finalHP, finalDEF, panelAP, req)
	}
	score := scoreDamageIndex*critFitFactor + scoreCritDmg*2 + weightedWords*req.WordCoef
	switch mode {
	case "MAX_CD":
		// Highest-CDMG mode still respects the requested crit line: among builds
		// near the target, high panel CDMG matters; but the primary key is the
		// crit-capped output index so the optimizer does not prefer unusable or
		// heavily over-capped crit layouts.
		score = scoreDamageIndex*critFitFactor*120 + scoreCritDmg*620 + finalATK*7 + weightedWords*120 - critPenalty*900000
	case "MAX_WORDS":
		// Effective rolls first, but keep enough output pressure that the result
		// does not become a pile of words with poor crit/output balance.
		score = weightedWords*120000 + scoreDamageIndex*critFitFactor*40 + scoreCritDmg*420 - critPenalty*900000
	case "RUPTURE_SHEER":
		// Rupture damage can crit. Prioritize Sheer Force through a crit-capped
		// output index, then use raw Sheer Force as a secondary key.
		score = scoreDamageIndex*critFitFactor*240 + sheerForce*300 + scoreCritDmg*450 + weightedWords*100 - critPenalty*900000
	case "ANOMALY_AP":
		score = panelAP + finalATK*0.000001 + weightedWords*0.001
	case "ANOMALY_ATK":
		score = finalATK + panelAP*0.001 + weightedWords*0.001
	case "ANOMALY_WORDS":
		score = weightedWords + panelAP*0.0001 + finalATK*0.0000001
	case "STRICT_TARGETS", "STRICT_TARGET":
		// Sort primarily by how close the user-entered fields are to their
		// targets in display order. Damage and words are only tiebreakers once target
		// fit is decided.
		score = -strictPenalty*1000000000 + scoreDamageIndex*critFitFactor*80 + weightedWords*180 + panelCritDmg*120 + finalATK*3 + finalHP*0.03 + finalDEF*0.3 + panelAP*15
	case "UTILITY_BALANCE":
		// Support/Stun/Defense thresholds are target windows rather than one-sided floors.
		// After matching selected targets, sort by useful words and visible panel value.
		score = weightedWords*100000 + panelCritDmg*180 + panelCritRate*140 + finalATK*6 + finalHP*0.25 + finalDEF*4 + stats["IMPACT"]*1200 + stats["ENERGY_REGEN"]*6000 - utilityPenalty*900000
	}

	buildCopy := append([]Disc{}, build...)
	sort.SliceStable(buildCopy, func(i, j int) bool { return buildCopy[i].Slot < buildCopy[j].Slot })
	goalText := critGoalStatusText(panelCritRate, req.TargetCritRate)
	displayWords := gameEffectiveWords

	res := OptimizeResult{
		Score:               round(score, 4),
		OutputScore:         round(damageIndex, 4),
		CritRate:            round(critRate, 3),
		CritDmg:             round(critDmg, 3),
		PanelCritRate:       round(panelCritRate, 3),
		PanelCritDmg:        round(panelCritDmg, 3),
		CritOverflow:        round(overflow, 3),
		CritShortfall:       round(critShortfall, 3),
		CritWaste:           round(critOver, 3),
		CritFitPenalty:      round(critPenalty, 6),
		CritFitFactor:       round(math.Max(0, 1-critPenalty), 4),
		EffectiveWords:      round(effWords, 3),
		WeightedWords:       round(weightedWords, 3),
		GameEffectiveWords:  round(gameEffectiveWords, 3),
		FinalAttack:         round(finalATK, 3),
		FinalHP:             round(finalHP, 3),
		FinalDefense:        round(finalDEF, 3),
		CombatFinalAttack:   round(combatFinalATK, 3),
		CombatFinalHP:       round(combatFinalHP, 3),
		CombatFinalDefense:  round(combatFinalDEF, 3),
		InitialEnergyRegen:  round(initialEnergyRegen, 3),
		FinalAnomalyMastery: round(finalAnomalyMastery, 3),
		SheerForce:          round(sheerForce, 3),
		CritMultiplier:      round(critMultiplier, 4),
		DamageIndex:         round(damageIndex, 3),
		Stats:               roundStats(stats),
		CombatStats:         roundStats(combatStats),
		SetSummary:          setCounts,
		Discs:               buildCopy,
	}
	switch mode {
	case "MAX_CD":
		if strings.EqualFold(strings.TrimSpace(req.RoleSystem), "ATTACK") || strings.EqualFold(strings.TrimSpace(req.RoleSystem), "STRONG") {
			res.Reason = fmt.Sprintf("强攻模式：%s；计入音擎/触发套装后的实战参考暴击率 %.1f%%；面板暴伤 %.1f%%；优先总暴伤，总攻击 %.0f，有效词条 %.2f。", goalText, critRate, panelCritDmg, finalATK, displayWords)
		} else if strings.EqualFold(strings.TrimSpace(req.RoleSystem), "RUPTURE") {
			res.Reason = fmt.Sprintf("命破模式：%s；计入音擎/触发套装后的实战参考暴击率 %.1f%%；面板暴伤 %.1f%%；优先总暴伤，贯穿力 %.0f，有效词条 %.2f，伤害指数 %.0f。", goalText, critRate, panelCritDmg, sheerForce, displayWords, damageIndex)
		} else {
			res.Reason = fmt.Sprintf("%s；本模式优先总暴伤，面板暴伤 %.1f%%，有效词条 %.2f。", goalText, panelCritDmg, displayWords)
		}
	case "MAX_WORDS":
		if strings.EqualFold(strings.TrimSpace(req.RoleSystem), "ATTACK") || strings.EqualFold(strings.TrimSpace(req.RoleSystem), "STRONG") {
			res.Reason = fmt.Sprintf("强攻模式：%s；计入音擎/触发套装后的实战参考暴击率 %.1f%%；优先有效词条，有效词条 %.2f，面板暴伤 %.1f%%，总攻击 %.0f。", goalText, critRate, displayWords, panelCritDmg, finalATK)
		} else if strings.EqualFold(strings.TrimSpace(req.RoleSystem), "RUPTURE") {
			res.Reason = fmt.Sprintf("命破模式：%s；计入音擎/触发套装后的实战参考暴击率 %.1f%%；优先有效词条，有效词条 %.2f，面板暴伤 %.1f%%，贯穿力 %.0f，伤害指数 %.0f。", goalText, critRate, displayWords, panelCritDmg, sheerForce, damageIndex)
		} else {
			res.Reason = fmt.Sprintf("%s；本模式优先有效词条，有效词条 %.2f，面板暴伤 %.1f%%。", goalText, displayWords, panelCritDmg)
		}
	case "RUPTURE_SHEER":
		res.Reason = fmt.Sprintf("命破模式：%s；计入音擎/触发套装后的实战参考暴击率 %.1f%%；按贯穿力×暴击期望综合排序，伤害指数 %.0f，贯穿力 %.0f，生命 %.0f，攻击 %.0f，面板暴伤 %.1f%%。", goalText, critRate, damageIndex, sheerForce, finalHP, finalATK, panelCritDmg)
	case "ANOMALY_AP":
		res.Reason = fmt.Sprintf("异常模式：优先面板异常精通，面板异常精通 %.1f，面板攻击 %.0f，有效词条 %.2f。%s", panelAP, finalATK, displayWords, characterCombatReasonSuffix(req, initialEnergyRegen, finalAnomalyMastery, combatStats))
	case "ANOMALY_ATK":
		res.Reason = fmt.Sprintf("异常模式：优先面板攻击，面板攻击 %.0f，面板异常精通 %.1f，有效词条 %.2f。%s", finalATK, panelAP, displayWords, characterCombatReasonSuffix(req, initialEnergyRegen, finalAnomalyMastery, combatStats))
	case "ANOMALY_WORDS":
		res.Reason = fmt.Sprintf("异常模式：优先综合词条，有效词条 %.2f，面板异常精通 %.1f，面板攻击 %.0f。%s", displayWords, panelAP, finalATK, characterCombatReasonSuffix(req, initialEnergyRegen, finalAnomalyMastery, combatStats))
	case "STRICT_TARGETS", "STRICT_TARGET":
		if len(strictParts) > 0 {
			res.Reason = fmt.Sprintf("严格指标模式：按用户填写属性顺序优先贴近目标；%s。完全贴近不可达时，自动按约 1 个副词条为单位向上/向下寻找最近方案。伤害指数 %.0f，有效词条 %.2f。", strings.Join(strictParts, "；"), damageIndex, displayWords)
		} else {
			res.Reason = fmt.Sprintf("严格指标模式：当前未填写具体目标，按综合输出和词条作为兜底排序；伤害指数 %.0f，有效词条 %.2f。", damageIndex, displayWords)
		}
	case "UTILITY_BALANCE":
		roleLabel := map[string]string{"STUN": "击破", "DEFENSE": "防护", "SUPPORT": "辅助"}[strings.ToUpper(strings.TrimSpace(req.RoleSystem))]
		if roleLabel == "" {
			roleLabel = "综合"
		}
		res.Reason = fmt.Sprintf("%s模式：按已设置的暴击率/生命值/防御力/攻击力目标窗口筛选，达标附近优先；总攻击 %.0f，生命 %.0f，防御 %.0f，面板暴击率 %.1f%%，有效词条 %.2f。", roleLabel, finalATK, finalHP, finalDEF, panelCritRate, displayWords)
	default:
		res.Reason = fmt.Sprintf("综合分 = 面板暴伤 %.1f + 有效词条 %.2f × %.2f - 面板暴击率溢出 %.1f × %.2f。", panelCritDmg, displayWords, req.WordCoef, overflow, req.OverflowPenalty)
	}
	return res, true
}

func characterCombatReasonSuffix(req OptimizeRequest, initialEnergyRegen, finalAnomalyMastery float64, combatStats map[string]float64) string {
	if !isVelina(req.CharacterName) {
		return ""
	}
	return fmt.Sprintf(" 维琳娜实战参考：初始能量自动回复 %.2f，核心增伤 %.1f%%，异常掌控 %.1f。", initialEnergyRegen, combatStats["VELINA_CORE_DMG_BONUS"], finalAnomalyMastery)
}

func critGoalStatusText(actual, target float64) string {
	if target <= 0 {
		return fmt.Sprintf("面板暴击率 %.1f%%", actual)
	}
	if actual+1e-9 >= target {
		over := actual - target
		if over <= critTargetTolerance+1e-9 {
			if over > critDisplayTolerance {
				return fmt.Sprintf("面板暴击率 %.1f%% 高于目标 %.1f%%（溢出 %.1f%%，未超过 1 个暴击率词条，保留但降权）", actual, target, over)
			}
			return fmt.Sprintf("面板暴击率 %.1f%% 接近角色界面目标 %.1f%%", actual, target)
		}
		return fmt.Sprintf("面板暴击率 %.1f%% 高于目标 %.1f%%（溢出超过 1 个暴击率词条）", actual, target)
	}
	shortfall := target - actual
	if shortfall <= critTargetTolerance+1e-9 {
		return fmt.Sprintf("面板暴击率 %.1f%% 低于目标 %.1f%%（差 %.1f%%，未超过 1 个暴击率词条，保留但降权）", actual, target, shortfall)
	}
	return fmt.Sprintf("面板暴击率 %.1f%% 未达到角色界面目标 %.1f%%（低于目标超过 1 个暴击率词条）", actual, target)
}

func discMainStat(d Disc) StatValue {
	if strings.TrimSpace(d.MainStat.Type) != "" {
		return d.MainStat
	}
	if len(d.Stats) > 0 && strings.TrimSpace(d.Stats[0].Type) != "" {
		return d.Stats[0]
	}
	return StatValue{}
}

func normalizeCalculatedSubStat(s StatValue) StatValue {
	t := strings.TrimSpace(s.Type)
	v := s.Value
	// OCR can sometimes capture the orange enhancement marker (+1/+2/+3) instead
	// of the final white value. For sub-stats whose real values are fractional or
	// large fixed rolls, integer values 1..5 are impossible as final values, so we
	// safely convert them back to the S-rank roll value.
	if math.Abs(v-math.Round(v)) > 1e-9 || v < 1 || v > 5 {
		return s
	}
	roll := rollValue[t]
	if roll <= 0 {
		return s
	}
	switch t {
	case "CRIT_RATE", "CRIT_DMG", "DEF_PERCENT", "PEN_FLAT", "ANOMALY_PROFICIENCY":
		s.Value = (math.Round(v) + 1) * roll
	case "ATK_PERCENT", "HP_PERCENT":
		// 3% is a valid unenhanced final roll, so do not reinterpret a literal 3.
		if int(math.Round(v)) != 3 {
			s.Value = (math.Round(v) + 1) * roll
		}
	case "ATK_FLAT", "HP_FLAT", "DEF_FLAT":
		s.Value = (math.Round(v) + 1) * roll
	}
	return s
}

func discAllStats(d Disc) []StatValue {
	out := []StatValue{}
	if strings.TrimSpace(d.MainStat.Type) != "" {
		out = append(out, d.MainStat)
	}
	for _, s := range d.SubStats {
		if strings.TrimSpace(s.Type) != "" {
			out = append(out, normalizeCalculatedSubStat(s))
		}
	}
	if len(out) > 0 {
		return out
	}
	for i, s := range d.Stats {
		if strings.TrimSpace(s.Type) != "" {
			if i > 0 {
				s = normalizeCalculatedSubStat(s)
			}
			out = append(out, s)
		}
	}
	return out
}

func discWordStats(d Disc) []StatValue {
	out := []StatValue{}
	for _, s := range d.SubStats {
		if strings.TrimSpace(s.Type) != "" {
			out = append(out, normalizeCalculatedSubStat(s))
		}
	}
	if len(out) > 0 {
		return out
	}
	if len(d.Stats) > 1 {
		for _, s := range d.Stats[1:] {
			if strings.TrimSpace(s.Type) != "" {
				out = append(out, normalizeCalculatedSubStat(s))
			}
		}
	}
	return out
}

func calcFinalStatRaw(baseValue float64, baseBonus float64, percentBonus float64, additiveBonus float64) float64 {
	return (baseValue+baseBonus)*(1+percentBonus/100) + additiveBonus
}

func calcFinalAttack(baseValue float64, baseBonus float64, percentBonus float64, additiveBonus float64) float64 {
	// The in-game details panel truncates final attack in the test case provided.
	return math.Floor(calcFinalStatRaw(baseValue, baseBonus, percentBonus, additiveBonus) + 1e-9)
}

func calcFinalHP(baseValue float64, baseBonus float64, percentBonus float64, additiveBonus float64) float64 {
	// HP in the details panel rounds upward in the provided 60级仪玄 verification case.
	return math.Ceil(calcFinalStatRaw(baseValue, baseBonus, percentBonus, additiveBonus) - 1e-9)
}

func calcFinalDefense(baseValue float64, baseBonus float64, percentBonus float64, additiveBonus float64) float64 {
	// Defense follows the integer display used by the in-game agent details panel.
	return math.Floor(calcFinalStatRaw(baseValue, baseBonus, percentBonus, additiveBonus) + 1e-9)
}

func cloneStatMap(src map[string]float64) map[string]float64 {
	out := map[string]float64{}
	for k, v := range src {
		out[k] = v
	}
	return out
}

func canonicalSetName(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if name, ok := builtinSetNameLookup[raw]; ok {
		return name
	}
	compact := compactHan(raw)
	if name, ok := builtinSetNameLookup[compact]; ok {
		return name
	}
	if name, ok := bestSetName(raw); ok {
		return name
	}
	return raw
}

func elementDamageStatKey(element string) string {
	switch strings.ToUpper(strings.TrimSpace(element)) {
	case "FIRE":
		return "FIRE_DMG"
	case "ICE":
		return "ICE_DMG"
	case "ELECTRIC":
		return "ELECTRIC_DMG"
	case "PHYSICAL":
		return "PHYSICAL_DMG"
	case "ETHER":
		return "ETHER_DMG"
	case "WIND":
		return "WIND_DMG"
	case "LUMIFLUX", "LUMEN":
		return "LUMIFLUX_DMG"
	default:
		return ""
	}
}

func combatDamageBonusPercent(stats map[string]float64, element string) float64 {
	// A compact output-index proxy: include generic damage bonuses and the
	// selected agent's own elemental damage. For legacy requests without an
	// element, preserve the pre-v1.12 behavior by summing all elemental keys.
	total := stats["ELEMENT_DMG"] + stats["VELINA_CORE_DMG_BONUS"]
	if key := elementDamageStatKey(element); key != "" {
		return total + stats[key]
	}
	for _, key := range []string{"FIRE_DMG", "ICE_DMG", "ELECTRIC_DMG", "PHYSICAL_DMG", "ETHER_DMG", "WIND_DMG", "LUMIFLUX_DMG"} {
		total += stats[key]
	}
	return total
}

func calcCritMultiplier(critRate float64, critDmg float64) float64 {
	if critRate < 0 {
		critRate = 0
	}
	if critRate > 100 {
		critRate = 100
	}
	if critDmg < 0 {
		critDmg = 0
	}
	return 1 + (critRate/100)*(critDmg/100)
}

func applyTwoPiecePanelBonuses(stats map[string]float64, setCounts map[string]int) {
	for setName, count := range setCounts {
		if count < 2 {
			continue
		}
		for _, bonus := range twoPiecePanelBonuses[canonicalSetName(setName)] {
			addStat(stats, bonus)
		}
	}
}

func applyConditionalFourPiecePanelBonuses(stats map[string]float64, setCounts map[string]int, element string) {
	// v1.18：继续按 Pro 校准口径，角色详情页面板只计入角色成长/核心、音擎基础与高级属性、驱动盘主副属性、2件套静态效果。
	// 4件套效果即使文字条件看似常驻，也统一放入实战参考，避免与游戏内“代理人信息”页面不一致。
	_ = stats
	_ = setCounts
	_ = element
}

func applyConditionalFourPieceCombatBonuses(stats map[string]float64, setCounts map[string]int, element string) {
	if setCounts[canonicalSetName("拂晓行纪")] >= 4 && strings.EqualFold(strings.TrimSpace(element), "ETHER") {
		addStat(stats, StatValue{Type: "CRIT_DMG", Value: 30})
	}
	if setCounts[canonicalSetName("谶羽之誓")] >= 4 && (strings.EqualFold(strings.TrimSpace(element), "LUMIFLUX") || strings.EqualFold(strings.TrimSpace(element), "LUMEN")) {
		// 仅作为实战参考展示，不并入通用伤害指数，避免把“属性异常伤害”误算成全部流明伤害。
		addStat(stats, StatValue{Type: "ANOMALY_DMG_BONUS", Value: 15})
	}
}

func applyFourPieceCombatBonuses(stats map[string]float64, setCounts map[string]int) {
	for setName, count := range setCounts {
		if count < 4 {
			continue
		}
		for _, bonus := range fourPieceCombatBonuses[canonicalSetName(setName)] {
			addStat(stats, bonus)
		}
	}
}

func isVelina(name string) bool {
	name = strings.ReplaceAll(strings.TrimSpace(name), "·", "")
	name = strings.ReplaceAll(name, " ", "")
	return strings.Contains(name, "维琳娜")
}

func applyCharacterCombatBonuses(combatStats, panelStats map[string]float64, req OptimizeRequest) (float64, float64) {
	initialEnergyRegen := 0.0
	if req.BaseEnergyRegen > 0 {
		initialEnergyRegen = req.BaseEnergyRegen * (1 + panelStats["ENERGY_REGEN"]/100)
		combatStats["INITIAL_ENERGY_REGEN"] = initialEnergyRegen
	}
	if isVelina(req.CharacterName) && initialEnergyRegen > 1.2 {
		steps := math.Floor((initialEnergyRegen - 1.2 + 1e-9) / 0.01)
		combatStats["VELINA_CORE_DMG_BONUS"] += math.Min(35, steps*0.21)
		combatStats["ANOMALY_MASTERY_FLAT"] += math.Min(84, steps*0.5)
	}
	finalAnomalyMastery := 0.0
	if req.BaseAnomalyMastery > 0 {
		finalAnomalyMastery = req.BaseAnomalyMastery*(1+combatStats["ANOMALY_MASTERY"]/100) + combatStats["ANOMALY_MASTERY_FLAT"]
		combatStats["FINAL_ANOMALY_MASTERY"] = finalAnomalyMastery
	}
	return initialEnergyRegen, finalAnomalyMastery
}

func isAnomalyMode(mode string) bool {
	switch strings.ToUpper(strings.TrimSpace(mode)) {
	case "ANOMALY_AP", "ANOMALY_ATK", "ANOMALY_WORDS":
		return true
	default:
		return false
	}
}

func statWords(s StatValue) float64 {
	if rv := rollValue[strings.TrimSpace(s.Type)]; rv > 0 {
		return s.Value / rv
	}
	return 0
}

func addStat(stats map[string]float64, s StatValue) {
	t := strings.TrimSpace(s.Type)
	if t == "" || s.Value == 0 {
		return
	}
	stats[t] += s.Value
}

func matchesSetConstraints(counts map[string]int, pattern string, required4 string, required2 string) bool {
	required4 = canonicalSetName(required4)
	required2 = canonicalSetName(required2)
	if required4 != "" && counts[required4] < 4 {
		return false
	}
	if required2 != "" && counts[required2] < 2 {
		return false
	}
	switch strings.TrimSpace(pattern) {
	case "4+2":
		for setA, countA := range counts {
			if countA >= 4 {
				for setB, countB := range counts {
					if setB != setA && countB >= 2 {
						return true
					}
				}
			}
		}
		return false
	case "2+2+2":
		pairs := 0
		for _, c := range counts {
			if c >= 2 {
				pairs++
			}
		}
		return pairs >= 3
	default:
		return true
	}
}

func appendNearMiss(list []OptimizeResult, candidate OptimizeResult, limit int) []OptimizeResult {
	list = append(list, candidate)
	sort.SliceStable(list, func(i, j int) bool {
		if math.Abs(list[i].PanelCritRate-list[j].PanelCritRate) > 1e-9 {
			return list[i].PanelCritRate > list[j].PanelCritRate
		}
		if math.Abs(list[i].DamageIndex-list[j].DamageIndex) > 1e-9 {
			return list[i].DamageIndex > list[j].DamageIndex
		}
		if math.Abs(list[i].PanelCritDmg-list[j].PanelCritDmg) > 1e-9 {
			return list[i].PanelCritDmg > list[j].PanelCritDmg
		}
		return list[i].WeightedWords > list[j].WeightedWords
	})
	if limit <= 0 {
		limit = 5
	}
	if len(list) > limit {
		list = append([]OptimizeResult{}, list[:limit]...)
	}
	return list
}

func sortResults(results []OptimizeResult, mode string) {
	mode = strings.ToUpper(strings.TrimSpace(mode))
	sort.SliceStable(results, func(i, j int) bool {
		a, b := results[i], results[j]
		// v22: MAX_CD returns the highest visible panel CDMG inside the ±1-roll crit window.
		// Composite score remains primary for MAX_WORDS and RUPTURE_SHEER, where the
		// selected target is an output/index rather than one visible stat.
		scoreFirst := mode == "RUPTURE_SHEER" || mode == "MAX_WORDS" || mode == "STRICT_TARGETS" || mode == "STRICT_TARGET"
		if scoreFirst {
			if !almostEqual(a.Score, b.Score) {
				return a.Score > b.Score
			}
		}
		switch mode {
		case "STRICT_TARGETS", "STRICT_TARGET":
			if !almostEqual(a.DamageIndex, b.DamageIndex) {
				return a.DamageIndex > b.DamageIndex
			}
			if !almostEqual(a.WeightedWords, b.WeightedWords) {
				return a.WeightedWords > b.WeightedWords
			}
			return a.Score > b.Score
		case "RUPTURE_SHEER":
			if !almostEqual(a.DamageIndex, b.DamageIndex) {
				return a.DamageIndex > b.DamageIndex
			}
			if !almostEqual(a.SheerForce, b.SheerForce) {
				return a.SheerForce > b.SheerForce
			}
			if !almostEqual(a.PanelCritDmg, b.PanelCritDmg) {
				return a.PanelCritDmg > b.PanelCritDmg
			}
			if !almostEqual(a.CritShortfall, b.CritShortfall) {
				return a.CritShortfall < b.CritShortfall
			}
			return a.CritOverflow < b.CritOverflow
		case "MAX_CD":
			if !almostEqual(a.PanelCritDmg, b.PanelCritDmg) {
				return a.PanelCritDmg > b.PanelCritDmg
			}
			if !almostEqual(a.DamageIndex, b.DamageIndex) {
				return a.DamageIndex > b.DamageIndex
			}
			if !almostEqual(a.CritShortfall, b.CritShortfall) {
				return a.CritShortfall < b.CritShortfall
			}
			if !almostEqual(a.CritOverflow, b.CritOverflow) {
				return a.CritOverflow < b.CritOverflow
			}
			return a.WeightedWords > b.WeightedWords
		case "MAX_WORDS":
			if !almostEqual(a.WeightedWords, b.WeightedWords) {
				return a.WeightedWords > b.WeightedWords
			}
			if !almostEqual(a.DamageIndex, b.DamageIndex) {
				return a.DamageIndex > b.DamageIndex
			}
			if !almostEqual(a.CritShortfall, b.CritShortfall) {
				return a.CritShortfall < b.CritShortfall
			}
			if !almostEqual(a.CritOverflow, b.CritOverflow) {
				return a.CritOverflow < b.CritOverflow
			}
			return a.PanelCritDmg > b.PanelCritDmg
		case "ANOMALY_AP":
			if !almostEqual(a.Stats["ANOMALY_PROFICIENCY"], b.Stats["ANOMALY_PROFICIENCY"]) {
				return a.Stats["ANOMALY_PROFICIENCY"] > b.Stats["ANOMALY_PROFICIENCY"]
			}
			if !almostEqual(a.FinalAttack, b.FinalAttack) {
				return a.FinalAttack > b.FinalAttack
			}
			if !almostEqual(a.WeightedWords, b.WeightedWords) {
				return a.WeightedWords > b.WeightedWords
			}
			return a.Score > b.Score
		case "ANOMALY_ATK":
			if !almostEqual(a.FinalAttack, b.FinalAttack) {
				return a.FinalAttack > b.FinalAttack
			}
			if !almostEqual(a.Stats["ANOMALY_PROFICIENCY"], b.Stats["ANOMALY_PROFICIENCY"]) {
				return a.Stats["ANOMALY_PROFICIENCY"] > b.Stats["ANOMALY_PROFICIENCY"]
			}
			if !almostEqual(a.WeightedWords, b.WeightedWords) {
				return a.WeightedWords > b.WeightedWords
			}
			return a.Score > b.Score
		case "ANOMALY_WORDS":
			if !almostEqual(a.WeightedWords, b.WeightedWords) {
				return a.WeightedWords > b.WeightedWords
			}
			if !almostEqual(a.Stats["ANOMALY_PROFICIENCY"], b.Stats["ANOMALY_PROFICIENCY"]) {
				return a.Stats["ANOMALY_PROFICIENCY"] > b.Stats["ANOMALY_PROFICIENCY"]
			}
			if !almostEqual(a.FinalAttack, b.FinalAttack) {
				return a.FinalAttack > b.FinalAttack
			}
			return a.Score > b.Score
		case "UTILITY_BALANCE":
			if !almostEqual(a.Score, b.Score) {
				return a.Score > b.Score
			}
			if !almostEqual(a.WeightedWords, b.WeightedWords) {
				return a.WeightedWords > b.WeightedWords
			}
			if !almostEqual(a.FinalAttack, b.FinalAttack) {
				return a.FinalAttack > b.FinalAttack
			}
			return a.FinalHP > b.FinalHP
		default:
			if !almostEqual(a.Score, b.Score) {
				return a.Score > b.Score
			}
			if !almostEqual(a.CritDmg, b.CritDmg) {
				return a.CritDmg > b.CritDmg
			}
			if !almostEqual(a.WeightedWords, b.WeightedWords) {
				return a.WeightedWords > b.WeightedWords
			}
			if !almostEqual(a.CritShortfall, b.CritShortfall) {
				return a.CritShortfall < b.CritShortfall
			}
			return a.CritOverflow < b.CritOverflow
		}
	})
}

func almostEqual(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

func round(v float64, places int) float64 {
	pow := math.Pow10(places)
	return math.Round(v*pow) / pow
}

func roundStats(src map[string]float64) map[string]float64 {
	out := map[string]float64{}
	for k, v := range src {
		out[k] = round(v, 3)
	}
	return out
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
	})
}

func newID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err == nil {
		return "disc_" + hex.EncodeToString(b)
	}
	return fmt.Sprintf("disc_%d", time.Now().UnixNano())
}

func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}
