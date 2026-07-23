package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	externalScannerVersion       = "1.0.45"
	externalScannerDownloadURL   = "https://github.com/ZztIsolation/ZZZ-Scanner.Next/releases/tag/scanner-1.0.45"
	externalScannerDiscIDPrefix  = "ocrscan:"
	maxExternalScannerExportSize = 16 << 20
)

type externalScannerExportDisc struct {
	Index    int                          `json:"序号"`
	Name     string                       `json:"名称"`
	Slot     int                          `json:"槽位"`
	Rarity   string                       `json:"品质"`
	Level    int                          `json:"等级"`
	MaxLevel int                          `json:"最大等级"`
	MainStat map[string]json.RawMessage   `json:"主属性"`
	SubStats []map[string]json.RawMessage `json:"副属性"`
}

type externalScannerPreview struct {
	PreviewHash string   `json:"previewHash"`
	Total       int      `json:"total"`
	Added       int      `json:"added"`
	Removed     int      `json:"removed"`
	Unchanged   int      `json:"unchanged"`
	Warnings    []string `json:"warnings,omitempty"`
}

type externalScannerStatus struct {
	JobID       string                  `json:"jobId"`
	Status      string                  `json:"status"`
	Message     string                  `json:"message"`
	StartedAt   string                  `json:"startedAt,omitempty"`
	FinishedAt  string                  `json:"finishedAt,omitempty"`
	Preview     *externalScannerPreview `json:"preview,omitempty"`
	DownloadURL string                  `json:"downloadUrl,omitempty"`
	Version     string                  `json:"scannerVersion"`
}

type externalScannerJob struct {
	externalScannerStatus
	sourceDiscs []Disc
	applyDiscs  []Disc
}

var externalScannerRuntime struct {
	mu  sync.RWMutex
	job externalScannerJob
}

type externalScannerApplyRequest struct {
	JobID       string `json:"jobId"`
	PreviewHash string `json:"previewHash"`
}

func handleExternalScannerStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !authorizeScanImport(w, r) {
		return
	}
	if _, ok := startExternalScanner(); !ok {
		externalScannerRuntime.mu.RLock()
		status := externalScannerRuntime.job.externalScannerStatus
		externalScannerRuntime.mu.RUnlock()
		if status.Status == "running" {
			writeError(w, http.StatusConflict, "扫描器已经在运行，请等待当前扫描完成。")
			return
		}
		writeError(w, http.StatusFailedDependency, status.Message)
		return
	}
	externalScannerRuntime.mu.RLock()
	status := externalScannerRuntime.job.externalScannerStatus
	externalScannerRuntime.mu.RUnlock()
	writeJSON(w, status)
}

func handleExternalScannerStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !authorizeScanImport(w, r) {
		return
	}
	externalScannerRuntime.mu.RLock()
	status := externalScannerRuntime.job.externalScannerStatus
	externalScannerRuntime.mu.RUnlock()
	if status.Status == "" {
		status = externalScannerStatus{
			Status:      "idle",
			Message:     "扫描器尚未启动。",
			Version:     externalScannerVersion,
			DownloadURL: externalScannerDownloadURL,
		}
	}
	writeJSON(w, status)
}

func handleExternalScannerApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !authorizeScanImport(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request externalScannerApplyRequest
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "扫描确认数据格式错误："+err.Error())
		return
	}
	if err := ensureJSONEOF(decoder); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	externalScannerRuntime.mu.RLock()
	job := externalScannerRuntime.job
	externalScannerRuntime.mu.RUnlock()
	if job.Status != "ready" || request.JobID == "" || request.JobID != job.JobID {
		writeError(w, http.StatusConflict, "扫描结果已失效，请重新扫描。")
		return
	}
	if job.Preview == nil || request.PreviewHash == "" || request.PreviewHash != job.Preview.PreviewHash {
		writeError(w, http.StatusConflict, "扫描确认码不匹配，请重新扫描。")
		return
	}

	srvState.mu.Lock()
	currentHash, err := externalScannerPreviewHash(job.sourceDiscs, srvState.state)
	if err != nil {
		srvState.mu.Unlock()
		writeError(w, http.StatusInternalServerError, "无法校验扫描结果："+err.Error())
		return
	}
	if currentHash != job.Preview.PreviewHash {
		srvState.mu.Unlock()
		writeError(w, http.StatusConflict, "扫描完成后库存发生了变化，请重新扫描。")
		return
	}
	applyExternalScannerDiscs(&srvState.state, job.applyDiscs)
	if err := saveState(srvState.storagePath, srvState.state); err != nil {
		srvState.mu.Unlock()
		writeError(w, http.StatusInternalServerError, "保存扫描库存失败："+err.Error())
		return
	}
	srvState.mu.Unlock()

	now := time.Now().UTC().Format(time.RFC3339)
	externalScannerRuntime.mu.Lock()
	if externalScannerRuntime.job.JobID == job.JobID {
		externalScannerRuntime.job.Status = "completed"
		externalScannerRuntime.job.Message = fmt.Sprintf("已导入 %d 个驱动盘。", len(job.applyDiscs))
		externalScannerRuntime.job.FinishedAt = now
	}
	externalScannerRuntime.mu.Unlock()
	writeJSON(w, map[string]any{"ok": true, "preview": job.Preview})
}

func startExternalScanner() (string, bool) {
	externalScannerRuntime.mu.Lock()
	defer externalScannerRuntime.mu.Unlock()
	if externalScannerRuntime.job.Status == "running" {
		return externalScannerRuntime.job.JobID, false
	}
	executable, err := findExternalScannerExecutable()
	if err != nil {
		externalScannerRuntime.job = externalScannerJob{externalScannerStatus: externalScannerStatus{
			Status:      "missing",
			Message:     err.Error(),
			FinishedAt:  time.Now().UTC().Format(time.RFC3339),
			DownloadURL: externalScannerDownloadURL,
			Version:     externalScannerVersion,
		}}
		return "", false
	}
	outputRoot, err := externalScannerOutputRoot()
	if err != nil {
		externalScannerRuntime.job = externalScannerJob{externalScannerStatus: externalScannerStatus{
			Status:      "failed",
			Message:     "无法创建扫描输出目录：" + err.Error(),
			FinishedAt:  time.Now().UTC().Format(time.RFC3339),
			DownloadURL: externalScannerDownloadURL,
			Version:     externalScannerVersion,
		}}
		return "", false
	}
	jobID := fmt.Sprintf("ocrscan-%d", time.Now().UnixNano())
	externalScannerRuntime.job = externalScannerJob{externalScannerStatus: externalScannerStatus{
		JobID:       jobID,
		Status:      "running",
		Message:     "扫描器已启动，请保持游戏前台并停留在驱动盘仓库。",
		StartedAt:   time.Now().UTC().Format(time.RFC3339),
		DownloadURL: externalScannerDownloadURL,
		Version:     externalScannerVersion,
	}}
	go runExternalScanner(jobID, executable, outputRoot)
	return jobID, true
}

func runExternalScanner(jobID, executable, outputRoot string) {
	cmd := exec.Command(executable,
		"--scan-once",
		"--max-items", "0",
		"--fast-mode",
		"--capture-mode", "dxgi",
		"--overlap-conflict-mode", "recover",
		"--output-root", outputRoot,
	)
	cmd.Dir = filepath.Dir(executable)
	output, err := cmd.CombinedOutput()
	if err != nil {
		message := externalScannerFailureMessage(err, output)
		finishExternalScannerJob(jobID, "failed", message, nil, nil, nil)
		return
	}
	exportFile := scannerOutputValue(output, "export_file")
	if exportFile == "" {
		exportFile = findLatestExternalScannerExport(outputRoot)
	}
	if exportFile == "" {
		finishExternalScannerJob(jobID, "failed", "扫描器已退出，但没有找到 export.json。请查看扫描器日志。", nil, nil, nil)
		return
	}
	if !isCompleteExternalScannerExport(exportFile) {
		finishExternalScannerJob(jobID, "failed", "扫描没有完整结束，已保留扫描日志但不会覆盖库存。", nil, nil, nil)
		return
	}
	if err := ensurePathWithin(outputRoot, exportFile); err != nil {
		finishExternalScannerJob(jobID, "failed", "扫描器返回了无效的结果路径："+err.Error(), nil, nil, nil)
		return
	}
	sourceDiscs, err := readExternalScannerExport(exportFile)
	if err != nil {
		finishExternalScannerJob(jobID, "failed", "无法读取扫描结果："+err.Error(), nil, nil, nil)
		return
	}

	srvState.mu.RLock()
	preview, applyDiscs, err := buildExternalScannerPreview(sourceDiscs, srvState.state)
	srvState.mu.RUnlock()
	if err != nil {
		finishExternalScannerJob(jobID, "failed", "扫描结果无法导入："+err.Error(), nil, nil, nil)
		return
	}
	message := fmt.Sprintf("扫描完成：识别 %d 个，新增 %d 个，移除 %d 个。", preview.Total, preview.Added, preview.Removed)
	finishExternalScannerJob(jobID, "ready", message, &preview, sourceDiscs, applyDiscs)
}

func isCompleteExternalScannerExport(path string) bool {
	return strings.EqualFold(filepath.Base(path), "export.json")
}

func finishExternalScannerJob(jobID, status, message string, preview *externalScannerPreview, sourceDiscs, applyDiscs []Disc) {
	externalScannerRuntime.mu.Lock()
	defer externalScannerRuntime.mu.Unlock()
	if externalScannerRuntime.job.JobID != jobID {
		return
	}
	externalScannerRuntime.job.Status = status
	externalScannerRuntime.job.Message = message
	externalScannerRuntime.job.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	externalScannerRuntime.job.Preview = preview
	externalScannerRuntime.job.sourceDiscs = cloneDiscs(sourceDiscs)
	externalScannerRuntime.job.applyDiscs = cloneDiscs(applyDiscs)
}

func cloneDiscs(discs []Disc) []Disc {
	if discs == nil {
		return nil
	}
	cloned := make([]Disc, len(discs))
	for i, disc := range discs {
		cloned[i] = disc
		cloned[i].Stats = append([]StatValue(nil), disc.Stats...)
		cloned[i].SubStats = append([]StatValue(nil), disc.SubStats...)
	}
	return cloned
}

func externalScannerFailureMessage(runErr error, output []byte) string {
	if exitErr := new(exec.ExitError); errors.As(runErr, &exitErr) {
		switch exitErr.ExitCode() {
		case 73:
			return "已有一个扫描任务正在运行。"
		case 130:
			return "扫描已取消。"
		}
	}
	detail := lastNonEmptyScannerLine(output)
	if detail == "" {
		detail = runErr.Error()
	}
	return "扫描器运行失败：" + detail
}

func lastNonEmptyScannerLine(output []byte) string {
	lines := nonEmptyLines(string(output))
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "result_file=") {
			continue
		}
		if len([]rune(line)) > 300 {
			return string([]rune(line)[:300]) + "…"
		}
		return line
	}
	return ""
}

func scannerOutputValue(output []byte, key string) string {
	prefix := key + "="
	var value string
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, prefix) {
			value = strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return value
}

func findLatestExternalScannerExport(root string) string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	type candidate struct {
		path string
		mod  time.Time
	}
	var candidates []candidate
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(root, entry.Name(), "export.json")
		info, err := os.Stat(path)
		if err == nil {
			candidates = append(candidates, candidate{path: path, mod: info.ModTime()})
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].mod.After(candidates[j].mod) })
	if len(candidates) == 0 {
		return ""
	}
	return candidates[0].path
}

func ensurePathWithin(root, path string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(rootAbs, pathAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("结果文件不在指定输出目录中")
	}
	return nil
}

func externalScannerOutputRoot() (string, error) {
	dir, err := appConfigDir()
	if err != nil {
		return "", err
	}
	root := filepath.Join(dir, "scanner-output")
	if err := os.MkdirAll(root, 0755); err != nil {
		return "", err
	}
	return root, nil
}

func findExternalScannerExecutable() (string, error) {
	var bases []string
	if executable, err := os.Executable(); err == nil {
		bases = append(bases, filepath.Dir(executable))
	}
	if cwd, err := os.Getwd(); err == nil {
		bases = append(bases, cwd)
	}
	for _, base := range bases {
		for _, relative := range []string{
			filepath.Join("scan", "external", "ZZZ-Scanner.Next", "ZZZ-Scanner.Next.exe"),
			filepath.Join("scan", "ZZZ-Scanner.Next", "ZZZ-Scanner.Next.exe"),
			filepath.Join("ZZZ-Scanner.Next", "ZZZ-Scanner.Next.exe"),
		} {
			candidate := filepath.Join(base, relative)
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate, nil
			}
		}
	}
	return "", fmt.Errorf("未找到 ZZZ-Scanner.Next.exe。请使用 v1.15 完整版，或将扫描器 %s 放入 scan\\external\\ZZZ-Scanner.Next。", externalScannerVersion)
}

func readExternalScannerExport(path string) ([]Disc, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	limited := io.LimitReader(file, maxExternalScannerExportSize+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if len(data) > maxExternalScannerExportSize {
		return nil, errors.New("export.json 超过 16 MiB 安全限制")
	}
	var exported []externalScannerExportDisc
	if err := json.Unmarshal(data, &exported); err != nil {
		return nil, fmt.Errorf("JSON 格式错误：%w", err)
	}
	if len(exported) == 0 {
		return nil, errors.New("扫描结果为空")
	}
	if len(exported) > 5000 {
		return nil, fmt.Errorf("扫描结果包含 %d 个驱动盘，超过 5000 个安全限制", len(exported))
	}
	discs := make([]Disc, 0, len(exported))
	for i, exportedDisc := range exported {
		disc, err := convertExternalScannerDisc(exportedDisc)
		if err != nil {
			return nil, fmt.Errorf("第 %d 条扫描结果：%w", i+1, err)
		}
		discs = append(discs, disc)
	}
	return discs, nil
}

func convertExternalScannerDisc(exported externalScannerExportDisc) (Disc, error) {
	name := strings.TrimSpace(exported.Name)
	if name == "" {
		return Disc{}, errors.New("套装名称为空")
	}
	if exported.Slot < 1 || exported.Slot > 6 {
		return Disc{}, fmt.Errorf("槽位必须是 1-6，实际为 %d", exported.Slot)
	}
	rarity := strings.ToUpper(strings.TrimSpace(exported.Rarity))
	if rarity == "" {
		return Disc{}, errors.New("品质为空")
	}
	if exported.Level < 0 || exported.Level > exported.MaxLevel || exported.MaxLevel <= 0 {
		return Disc{}, fmt.Errorf("等级无效：%d/%d", exported.Level, exported.MaxLevel)
	}
	mainStat, err := oneExternalScannerStat(exported.MainStat, exported.Slot, true)
	if err != nil {
		return Disc{}, fmt.Errorf("主属性：%w", err)
	}
	if len(exported.SubStats) > 4 {
		return Disc{}, fmt.Errorf("副属性超过 4 条：%d", len(exported.SubStats))
	}
	subStats := make([]StatValue, 0, len(exported.SubStats))
	seen := map[string]bool{}
	for i, raw := range exported.SubStats {
		stat, err := oneExternalScannerStat(raw, exported.Slot, false)
		if err != nil {
			return Disc{}, fmt.Errorf("副属性 %d：%w", i+1, err)
		}
		if seen[stat.Type] {
			return Disc{}, fmt.Errorf("副属性类型重复：%s", stat.Type)
		}
		seen[stat.Type] = true
		subStats = append(subStats, stat)
	}
	stats := append([]StatValue{mainStat}, subStats...)
	return Disc{
		SetName:  name,
		Slot:     exported.Slot,
		Rarity:   rarity,
		Level:    exported.Level,
		Stats:    stats,
		MainStat: mainStat,
		SubStats: subStats,
	}, nil
}

func oneExternalScannerStat(raw map[string]json.RawMessage, slot int, main bool) (StatValue, error) {
	if len(raw) != 1 {
		return StatValue{}, fmt.Errorf("应当只有 1 个词条，实际为 %d", len(raw))
	}
	for name, encoded := range raw {
		value, percent, err := externalScannerStatNumber(encoded)
		if err != nil {
			return StatValue{}, fmt.Errorf("%s 数值无效：%w", name, err)
		}
		statType, err := externalScannerStatType(name, slot, main, percent)
		if err != nil {
			return StatValue{}, err
		}
		return StatValue{Type: statType, Value: value}, nil
	}
	return StatValue{}, errors.New("词条为空")
}

func externalScannerStatNumber(raw json.RawMessage) (float64, bool, error) {
	var number float64
	if err := json.Unmarshal(raw, &number); err == nil {
		if !isFinite(number) {
			return 0, false, errors.New("不是有限数字")
		}
		return number, false, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return 0, false, errors.New("必须是数字或百分比字符串")
	}
	text = strings.TrimSpace(text)
	percent := strings.HasSuffix(text, "%")
	text = strings.TrimSpace(strings.TrimSuffix(text, "%"))
	number, err := strconv.ParseFloat(text, 64)
	if err != nil || !isFinite(number) {
		return 0, percent, errors.New("无法解析数值")
	}
	return number, percent, nil
}

func isFinite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func externalScannerStatType(name string, slot int, main, percent bool) (string, error) {
	name = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(name), "%"))
	switch name {
	case "生命值":
		if percent || main && slot >= 4 {
			return "HP_PERCENT", nil
		}
		return "HP_FLAT", nil
	case "攻击力":
		if percent || main && slot >= 4 {
			return "ATK_PERCENT", nil
		}
		return "ATK_FLAT", nil
	case "防御力":
		if percent || main && slot >= 4 {
			return "DEF_PERCENT", nil
		}
		return "DEF_FLAT", nil
	case "异常精通":
		return "ANOMALY_PROFICIENCY", nil
	case "暴击率":
		return "CRIT_RATE", nil
	case "暴击伤害":
		return "CRIT_DMG", nil
	case "穿透值":
		return "PEN_FLAT", nil
	case "穿透率":
		return "PEN_RATIO", nil
	case "物理伤害加成":
		return "PHYSICAL_DMG", nil
	case "火属性伤害加成":
		return "FIRE_DMG", nil
	case "冰属性伤害加成":
		return "ICE_DMG", nil
	case "电属性伤害加成":
		return "ELECTRIC_DMG", nil
	case "以太伤害加成":
		return "ETHER_DMG", nil
	case "风属性伤害加成":
		return "WIND_DMG", nil
	case "能量自动回复":
		return "ENERGY_REGEN", nil
	case "异常掌控":
		return "ANOMALY_MASTERY", nil
	case "冲击力":
		return "IMPACT", nil
	default:
		return "", fmt.Errorf("不支持的词条名称：%s", name)
	}
}

func buildExternalScannerPreview(sourceDiscs []Disc, state AppState) (externalScannerPreview, []Disc, error) {
	hash, err := externalScannerPreviewHash(sourceDiscs, state)
	if err != nil {
		return externalScannerPreview{}, nil, err
	}
	existingByFingerprint := map[string][]Disc{}
	existingCount := 0
	for _, disc := range state.Discs {
		if !strings.HasPrefix(disc.ID, externalScannerDiscIDPrefix) {
			continue
		}
		fingerprint := externalScannerDiscFingerprint(disc)
		existingByFingerprint[fingerprint] = append(existingByFingerprint[fingerprint], disc)
		existingCount++
	}
	for fingerprint := range existingByFingerprint {
		sort.Slice(existingByFingerprint[fingerprint], func(i, j int) bool {
			return existingByFingerprint[fingerprint][i].ID < existingByFingerprint[fingerprint][j].ID
		})
	}
	preview := externalScannerPreview{
		PreviewHash: hash,
		Total:       len(sourceDiscs),
		Warnings:    []string{"视觉扫描结果不包含游戏内 UID、锁定状态或装备角色；已匹配记录的人工信息会保留。"},
	}
	stamp := time.Now().UTC().Format(time.RFC3339)
	applyDiscs := make([]Disc, 0, len(sourceDiscs))
	for _, source := range sourceDiscs {
		fingerprint := externalScannerDiscFingerprint(source)
		matches := existingByFingerprint[fingerprint]
		if len(matches) > 0 {
			existing := matches[0]
			existingByFingerprint[fingerprint] = matches[1:]
			existing.SetName = source.SetName
			existing.Slot = source.Slot
			existing.Rarity = source.Rarity
			existing.Level = source.Level
			existing.Stats = append([]StatValue(nil), source.Stats...)
			existing.MainStat = source.MainStat
			existing.SubStats = append([]StatValue(nil), source.SubStats...)
			applyDiscs = append(applyDiscs, existing)
			preview.Unchanged++
			continue
		}
		source.ID = externalScannerDiscIDPrefix + strings.TrimPrefix(newID(), "disc_")
		source.CreatedAt = stamp
		source.UpdatedAt = stamp
		applyDiscs = append(applyDiscs, source)
		preview.Added++
	}
	preview.Removed = existingCount - preview.Unchanged
	return preview, applyDiscs, nil
}

func externalScannerPreviewHash(sourceDiscs []Disc, state AppState) (string, error) {
	payload := struct {
		SourceDiscs     []Disc           `json:"sourceDiscs"`
		ExistingDiscs   []Disc           `json:"existingDiscs"`
		CharacterBuilds []CharacterBuild `json:"characterBuilds"`
		DiscClaims      []DiscClaim      `json:"discClaims"`
	}{sourceDiscs, state.Discs, state.CharacterBuilds, state.DiscClaims}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func externalScannerDiscFingerprint(disc Disc) string {
	subStats := append([]StatValue(nil), disc.SubStats...)
	sort.Slice(subStats, func(i, j int) bool {
		if subStats[i].Type != subStats[j].Type {
			return subStats[i].Type < subStats[j].Type
		}
		return subStats[i].Value < subStats[j].Value
	})
	parts := []string{
		strings.TrimSpace(disc.SetName),
		strconv.Itoa(disc.Slot),
		strings.ToUpper(strings.TrimSpace(disc.Rarity)),
		strconv.Itoa(disc.Level),
		disc.MainStat.Type + "=" + strconv.FormatFloat(disc.MainStat.Value, 'f', 4, 64),
	}
	for _, stat := range subStats {
		parts = append(parts, stat.Type+"="+strconv.FormatFloat(stat.Value, 'f', 4, 64))
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:])
}

func applyExternalScannerDiscs(state *AppState, incoming []Disc) {
	kept := make([]Disc, 0, len(state.Discs)+len(incoming))
	for _, disc := range state.Discs {
		if !strings.HasPrefix(disc.ID, externalScannerDiscIDPrefix) {
			kept = append(kept, disc)
		}
	}
	kept = append(kept, cloneDiscs(incoming)...)
	state.Discs = kept
	state.Version = appVersion
	live := make(map[string]bool, len(kept))
	for _, disc := range kept {
		live[disc.ID] = true
	}
	claims := state.DiscClaims[:0]
	for _, claim := range state.DiscClaims {
		if live[claim.DiscID] {
			claims = append(claims, claim)
		}
	}
	state.DiscClaims = claims
	for i := range state.CharacterBuilds {
		ids := state.CharacterBuilds[i].DiscIDs[:0]
		for _, id := range state.CharacterBuilds[i].DiscIDs {
			if live[id] {
				ids = append(ids, id)
			}
		}
		state.CharacterBuilds[i].DiscIDs = ids
	}
}
