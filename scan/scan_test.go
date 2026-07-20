package scan

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
	"time"
)

func TestDecodeAndBuildExport(t *testing.T) {
	adapter := testAdapter()
	mainProperty := wireMessage(
		varintField(adapter.Property.Key, 11101),
		varintField(adapter.Property.BaseValue, 2200),
	)
	subProperty := wireMessage(
		varintField(adapter.Property.Key, 12101),
		varintField(adapter.Property.BaseValue, 240),
		varintField(adapter.Property.AddValue, 240),
	)
	equip := wireMessage(
		varintField(adapter.Equip.UID, 987654),
		varintField(adapter.Equip.TemplateID, 31001),
		varintField(adapter.Equip.Level, 15),
		varintField(adapter.Equip.Star, 5),
		varintField(adapter.Equip.Lock, 1),
		bytesField(adapter.Equip.MainProperties, mainProperty),
		bytesField(adapter.Equip.SubProperties, subProperty),
	)
	body := wireMessage(bytesField(adapter.Response.EquipList, equip))

	response, err := DecodeEquipmentResponse(body, adapter, "test-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Equips) != 1 || response.Equips[0].UID != 987654 || !response.Equips[0].Lock {
		t.Fatalf("unexpected decoded response: %#v", response)
	}

	capturedAt := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	export, err := BuildExport(response, adapter, testCatalog(), "test-1", "10000001", capturedAt)
	if err != nil {
		t.Fatal(err)
	}
	if err := export.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := export.Discs[0]; got.ID != "zzz:987654" || got.SetName != "测试套装" || got.Slot != 1 ||
		got.MainStat.Value != 2200 || len(got.SubStats) != 1 || got.SubStats[0].Value != 4.8 {
		t.Fatalf("unexpected exported disc: %#v", got)
	}
}

func TestBuildExportRejectsUnknownTemplate(t *testing.T) {
	response := EquipmentResponse{Equips: []EquipInfo{{
		UID:            1,
		TemplateID:     99999,
		Level:          15,
		Star:           5,
		MainProperties: []EquipProperty{{Key: 11101, BaseValue: 2200}},
	}}}
	_, err := BuildExport(response, testAdapter(), testCatalog(), "test-1", "", time.Now())
	if err == nil || !strings.Contains(err.Error(), "unknown equipment template") {
		t.Fatalf("expected unknown-template error, got %v", err)
	}
}

func TestAdapterRejectsUnlistedVersion(t *testing.T) {
	err := testAdapter().Validate("test-2")
	if err == nil || !strings.Contains(err.Error(), "does not explicitly support") {
		t.Fatalf("expected version error, got %v", err)
	}
}

func TestDecodeFrame(t *testing.T) {
	header := []byte{1, 2}
	body := []byte{3, 4, 5}
	encoded := make([]byte, 12+len(header)+len(body)+4)
	binary.BigEndian.PutUint32(encoded[:4], FrameHeadMagic)
	binary.BigEndian.PutUint16(encoded[4:6], 1093)
	binary.BigEndian.PutUint16(encoded[6:8], uint16(len(header)))
	binary.BigEndian.PutUint32(encoded[8:12], uint32(len(body)))
	copy(encoded[12:], header)
	copy(encoded[12+len(header):], body)
	binary.BigEndian.PutUint32(encoded[len(encoded)-4:], FrameTailMagic)

	frame, err := DecodeFrame(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if frame.CommandID != 1093 || !bytes.Equal(frame.Header, header) || !bytes.Equal(frame.Body, body) {
		t.Fatalf("unexpected frame: %#v", frame)
	}

	encoded[len(encoded)-1] ^= 1
	if _, err := DecodeFrame(encoded); err == nil {
		t.Fatal("expected invalid tail magic to fail")
	}
}

func TestMT19937_64AndSessionPad(t *testing.T) {
	generator := newMT19937_64(5489)
	want := []uint64{
		14514284786278117030,
		4620546740167642908,
		13109570281517897720,
	}
	for i, expected := range want {
		if got := generator.next(); got != expected {
			t.Fatalf("output %d: got %d, want %d", i, got, expected)
		}
	}
	pad := DeriveSessionPad(100, 100^5489)
	if len(pad) != 4096 || binary.LittleEndian.Uint64(pad[:8]) != want[0] {
		t.Fatalf("unexpected session pad")
	}
	plain := []byte{1, 2, 3, 4}
	encrypted := append([]byte(nil), plain...)
	XORBytes(encrypted, pad)
	XORBytes(encrypted, pad)
	if !bytes.Equal(encrypted, plain) {
		t.Fatal("XOR stream must round trip")
	}
}

func testAdapter() Adapter {
	return Adapter{
		ID:             "test-adapter",
		Region:         "test",
		ClientVersions: []string{"test-1"},
		Command:        CommandLayout{GetEquipDataRequest: 1001, GetEquipDataResponse: 1002},
		Response:       ResponseLayout{Retcode: ScalarField{Number: 1, XOR: 91}, EquipList: 2},
		Equip: EquipLayout{
			UID:            ScalarField{Number: 1, XOR: 101},
			TemplateID:     ScalarField{Number: 2, XOR: 102},
			Level:          ScalarField{Number: 3, XOR: 103},
			Star:           ScalarField{Number: 4, XOR: 104},
			Lock:           ScalarField{Number: 5},
			MainProperties: 6,
			SubProperties:  7,
		},
		Property: PropertyLayout{
			Key:       ScalarField{Number: 1, XOR: 201},
			BaseValue: ScalarField{Number: 2, XOR: 202},
			AddValue:  ScalarField{Number: 3, XOR: 203},
		},
	}
}

func testCatalog() Catalog {
	return Catalog{
		ID:        "test-catalog",
		AdapterID: "test-adapter",
		Equipment: map[string]EquipmentTemplate{
			"31001": {SetName: "测试套装", Slot: 1, Rarity: "S"},
		},
		Properties: map[string]PropertyTemplate{
			"11101": {Type: "HP_FLAT", Scale: 1, Decimals: 0},
			"12101": {Type: "CRIT_RATE", Scale: 0.01, Decimals: 1},
		},
	}
}

func varintField(field ScalarField, value uint64) []byte {
	return rawVarintField(field.Number, value^field.XOR)
}

func rawVarintField(number int, value uint64) []byte {
	out := appendVarint(nil, uint64(number<<3))
	return appendVarint(out, value)
}

func bytesField(number int, value []byte) []byte {
	out := appendVarint(nil, uint64(number<<3|2))
	out = appendVarint(out, uint64(len(value)))
	return append(out, value...)
}

func wireMessage(fields ...[]byte) []byte {
	return bytes.Join(fields, nil)
}

func appendVarint(dst []byte, value uint64) []byte {
	var scratch [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(scratch[:], value)
	return append(dst, scratch[:n]...)
}
