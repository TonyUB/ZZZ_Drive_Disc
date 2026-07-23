// Package cn contains the CN official-client bootstrap that can be verified
// without reading game memory or extracting protected game metadata.
package cn

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	ProfileID       = "cnprodwin-3.0.0"
	ClientVersion   = "CNPRODWin3.0.0"
	GameVersion     = "3.0.0"
	DefaultGameDir  = `C:\Program Files\miHoYo Launcher\games\ZenlessZoneZero Game`
	DispatchURL     = "https://globaldp-prod-cn01.juequling.com/query_dispatch"
	GatewayURL      = "https://prod-gf-cn.juequling.com/query_gateway"
	RegionName      = "prod_gf_cn"
	RegionTitle     = "新艾利都"
	ChannelID       = 1
	SubChannelID    = 2
	Platform        = 3
	RSAVersion      = 3
	expectedExeHash = "4a1907c96d3325340027e7344c9a71de72607462bf218db727c7f77243af076f"
	expectedPkgHash = "e20ec1190ef326caf09d937e3256f5081623d8f33285330402a61998fc79cf2b"
)

type InstallFingerprint struct {
	ProfileID      string `json:"profileId"`
	GameDir        string `json:"gameDir"`
	GameVersion    string `json:"gameVersion"`
	VersionInfo    string `json:"versionInfo"`
	ChannelID      string `json:"channelId"`
	SubChannelID   string `json:"subChannelId"`
	CPS            string `json:"cps"`
	ExecutableHash string `json:"executableSha256"`
	PackageHash    string `json:"packageManifestSha256"`
	Verified       bool   `json:"verified"`
}

func InspectInstall(gameDir string) (InstallFingerprint, error) {
	if strings.TrimSpace(gameDir) == "" {
		gameDir = DefaultGameDir
	}
	abs, err := filepath.Abs(gameDir)
	if err != nil {
		return InstallFingerprint{}, err
	}
	config, err := readINI(filepath.Join(abs, "config.ini"))
	if err != nil {
		return InstallFingerprint{}, fmt.Errorf("read game config: %w", err)
	}
	versionBytes, err := os.ReadFile(filepath.Join(abs, "version_info"))
	if err != nil {
		return InstallFingerprint{}, fmt.Errorf("read version_info: %w", err)
	}
	exeHash, err := hashFile(filepath.Join(abs, "ZenlessZoneZero.exe"))
	if err != nil {
		return InstallFingerprint{}, fmt.Errorf("hash game executable: %w", err)
	}
	pkgHash, err := hashFile(filepath.Join(abs, "pkg_version"))
	if err != nil {
		return InstallFingerprint{}, fmt.Errorf("hash package manifest: %w", err)
	}
	fingerprint := InstallFingerprint{
		ProfileID:      ProfileID,
		GameDir:        abs,
		GameVersion:    config["game_version"],
		VersionInfo:    strings.TrimSpace(string(versionBytes)),
		ChannelID:      config["channel"],
		SubChannelID:   config["sub_channel"],
		CPS:            config["cps"],
		ExecutableHash: exeHash,
		PackageHash:    pkgHash,
	}
	fingerprint.Verified = fingerprint.GameVersion == GameVersion &&
		fingerprint.VersionInfo == ClientVersion && fingerprint.ChannelID == "1" &&
		fingerprint.SubChannelID == "2" && strings.EqualFold(fingerprint.ExecutableHash, expectedExeHash) &&
		strings.EqualFold(fingerprint.PackageHash, expectedPkgHash)
	if !fingerprint.Verified {
		return fingerprint, errors.New("installed client does not match the verified CNPRODWin3.0.0 fingerprint")
	}
	return fingerprint, nil
}

func readINI(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	values := make(map[string]string)
	scanner := bufio.NewScanner(io.LimitReader(file, 1<<20))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "[") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if ok {
			values[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	return values, scanner.Err()
}

func hashFile(path string) (string, error) {
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
