package main

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/TonyUB/ZZZ_Drive_Disc/scan"
)

const maxInputSize = 32 << 20

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return usageError()
	}
	switch args[0] {
	case "decode":
		return runDecode(args[1:])
	case "convert":
		return runConvert(args[1:])
	case "check-adapter":
		return runCheckAdapter(args[1:])
	case "help", "-h", "--help":
		printUsage()
		return nil
	default:
		return usageError()
	}
}

func runDecode(args []string) error {
	flags := flag.NewFlagSet("decode", flag.ContinueOnError)
	common := addCommonFlags(flags)
	bodyPath := flags.String("body", "", "decrypted GetEquipDataScRsp protobuf body")
	format := flags.String("body-format", "raw", "raw, hex, or base64")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *bodyPath == "" {
		return errors.New("--body is required")
	}
	adapter, catalog, err := loadInputs(common)
	if err != nil {
		return err
	}
	body, err := readLimited(*bodyPath)
	if err != nil {
		return err
	}
	switch strings.ToLower(*format) {
	case "raw":
	case "hex":
		body, err = hex.DecodeString(strings.TrimSpace(string(body)))
	case "base64":
		body, err = base64.StdEncoding.DecodeString(strings.TrimSpace(string(body)))
	default:
		return fmt.Errorf("unsupported --body-format %q", *format)
	}
	if err != nil {
		return fmt.Errorf("decode input body: %w", err)
	}
	response, err := scan.DecodeEquipmentResponse(body, adapter, common.clientVersion)
	if err != nil {
		return err
	}
	return writeExport(response, adapter, catalog, common)
}

func runConvert(args []string) error {
	flags := flag.NewFlagSet("convert", flag.ContinueOnError)
	common := addCommonFlags(flags)
	responsePath := flags.String("response", "", "decoded EquipmentResponse JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *responsePath == "" {
		return errors.New("--response is required")
	}
	adapter, catalog, err := loadInputs(common)
	if err != nil {
		return err
	}
	var response scan.EquipmentResponse
	if err := readJSON(*responsePath, &response); err != nil {
		return err
	}
	return writeExport(response, adapter, catalog, common)
}

func runCheckAdapter(args []string) error {
	flags := flag.NewFlagSet("check-adapter", flag.ContinueOnError)
	adapterPath := flags.String("adapter", "", "adapter JSON path")
	clientVersion := flags.String("client-version", "", "exact game client version")
	if err := flags.Parse(args); err != nil {
		return err
	}
	var adapter scan.Adapter
	if err := readJSON(*adapterPath, &adapter); err != nil {
		return err
	}
	if err := adapter.Validate(*clientVersion); err != nil {
		return err
	}
	fingerprint, err := adapter.Fingerprint()
	if err != nil {
		return err
	}
	fmt.Println(fingerprint)
	return nil
}

type commonFlags struct {
	adapterPath   string
	catalogPath   string
	clientVersion string
	accountUID    string
	outputPath    string
}

func addCommonFlags(flags *flag.FlagSet) *commonFlags {
	common := &commonFlags{}
	flags.StringVar(&common.adapterPath, "adapter", "", "version adapter JSON path")
	flags.StringVar(&common.catalogPath, "catalog", "", "equipment/property catalog JSON path")
	flags.StringVar(&common.clientVersion, "client-version", "", "exact game client version")
	flags.StringVar(&common.accountUID, "account-uid", "", "optional account UID to include")
	flags.StringVar(&common.outputPath, "output", "scan-result.json", "output path or - for stdout")
	return common
}

func loadInputs(common *commonFlags) (scan.Adapter, scan.Catalog, error) {
	if common.adapterPath == "" || common.catalogPath == "" || common.clientVersion == "" {
		return scan.Adapter{}, scan.Catalog{}, errors.New("--adapter, --catalog and --client-version are required")
	}
	var adapter scan.Adapter
	if err := readJSON(common.adapterPath, &adapter); err != nil {
		return scan.Adapter{}, scan.Catalog{}, err
	}
	var catalog scan.Catalog
	if err := readJSON(common.catalogPath, &catalog); err != nil {
		return scan.Adapter{}, scan.Catalog{}, err
	}
	return adapter, catalog, nil
}

func writeExport(response scan.EquipmentResponse, adapter scan.Adapter, catalog scan.Catalog, common *commonFlags) error {
	export, err := scan.BuildExport(response, adapter, catalog, common.clientVersion, common.accountUID, time.Now().UTC())
	if err != nil {
		return err
	}
	var writer io.Writer = os.Stdout
	var file *os.File
	if common.outputPath != "-" {
		file, err = os.Create(common.outputPath)
		if err != nil {
			return err
		}
		defer file.Close()
		writer = file
	}
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(export); err != nil {
		return err
	}
	if file != nil {
		fmt.Printf("已导出 %d 个驱动盘到 %s\n", len(export.Discs), common.outputPath)
	}
	return nil
}

func readJSON(path string, target any) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("input path is required")
	}
	b, err := readLimited(path)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(strings.NewReader(string(b)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

func readLimited(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reader := io.LimitReader(file, maxInputSize+1)
	b, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if len(b) > maxInputSize {
		return nil, fmt.Errorf("input exceeds %d bytes", maxInputSize)
	}
	return b, nil
}

func usageError() error {
	printUsage()
	return errors.New("choose decode, convert, or check-adapter")
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `zzz-drive-scan

Commands:
  decode         decode a decrypted GetEquipDataScRsp protobuf body
  convert        convert a decoded EquipmentResponse JSON fixture
  check-adapter  validate and fingerprint a version adapter

Run a command with -h for its flags.`)
	_ = context.Background() // keep the command package ready for active Sources
}
