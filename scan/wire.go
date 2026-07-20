package scan

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

type wireValue struct {
	wireType uint64
	varint   uint64
	bytes    []byte
}

// DecodeEquipmentResponse decodes the protobuf body of GetEquipDataScRsp with
// a validated, version-specific Adapter. It is deliberately independent of
// generated game protocol code.
func DecodeEquipmentResponse(body []byte, adapter Adapter, clientVersion string) (EquipmentResponse, error) {
	if err := adapter.Validate(clientVersion); err != nil {
		return EquipmentResponse{}, err
	}
	fields, err := decodeWireMessage(body)
	if err != nil {
		return EquipmentResponse{}, fmt.Errorf("decode GetEquipDataScRsp: %w", err)
	}
	retcode, err := scalar(fields, adapter.Response.Retcode, false)
	if err != nil && !errors.Is(err, errFieldMissing) {
		return EquipmentResponse{}, fmt.Errorf("decode retcode: %w", err)
	}
	response := EquipmentResponse{Retcode: int32(uint32(retcode))}
	for _, encoded := range bytesFields(fields, adapter.Response.EquipList) {
		equip, err := decodeEquip(encoded, adapter)
		if err != nil {
			return EquipmentResponse{}, err
		}
		response.Equips = append(response.Equips, equip)
	}
	return response, nil
}

func decodeEquip(encoded []byte, adapter Adapter) (EquipInfo, error) {
	fields, err := decodeWireMessage(encoded)
	if err != nil {
		return EquipInfo{}, fmt.Errorf("decode EquipInfo: %w", err)
	}
	uid, err := requiredScalar(fields, adapter.Equip.UID)
	if err != nil {
		return EquipInfo{}, fmt.Errorf("decode EquipInfo.uid: %w", err)
	}
	templateID, err := requiredScalar(fields, adapter.Equip.TemplateID)
	if err != nil {
		return EquipInfo{}, fmt.Errorf("decode EquipInfo.id: %w", err)
	}
	level, err := requiredScalar(fields, adapter.Equip.Level)
	if err != nil {
		return EquipInfo{}, fmt.Errorf("decode EquipInfo.level: %w", err)
	}
	star, err := requiredScalar(fields, adapter.Equip.Star)
	if err != nil {
		return EquipInfo{}, fmt.Errorf("decode EquipInfo.star: %w", err)
	}
	lock, err := scalar(fields, adapter.Equip.Lock, false)
	if err != nil && !errors.Is(err, errFieldMissing) {
		return EquipInfo{}, fmt.Errorf("decode EquipInfo.lock: %w", err)
	}
	equip := EquipInfo{
		UID:        uint32(uid),
		TemplateID: uint32(templateID),
		Level:      uint32(level),
		Star:       uint32(star),
		Lock:       lock != 0,
	}
	for _, encodedProperty := range bytesFields(fields, adapter.Equip.MainProperties) {
		property, err := decodeProperty(encodedProperty, adapter.Property)
		if err != nil {
			return EquipInfo{}, fmt.Errorf("decode main property: %w", err)
		}
		equip.MainProperties = append(equip.MainProperties, property)
	}
	for _, encodedProperty := range bytesFields(fields, adapter.Equip.SubProperties) {
		property, err := decodeProperty(encodedProperty, adapter.Property)
		if err != nil {
			return EquipInfo{}, fmt.Errorf("decode sub property: %w", err)
		}
		equip.SubProperties = append(equip.SubProperties, property)
	}
	return equip, nil
}

func decodeProperty(encoded []byte, layout PropertyLayout) (EquipProperty, error) {
	fields, err := decodeWireMessage(encoded)
	if err != nil {
		return EquipProperty{}, err
	}
	key, err := requiredScalar(fields, layout.Key)
	if err != nil {
		return EquipProperty{}, fmt.Errorf("key: %w", err)
	}
	base, err := requiredScalar(fields, layout.BaseValue)
	if err != nil {
		return EquipProperty{}, fmt.Errorf("baseValue: %w", err)
	}
	add, err := scalar(fields, layout.AddValue, false)
	if err != nil && !errors.Is(err, errFieldMissing) {
		return EquipProperty{}, fmt.Errorf("addValue: %w", err)
	}
	return EquipProperty{Key: uint32(key), BaseValue: uint32(base), AddValue: uint32(add)}, nil
}

var errFieldMissing = errors.New("protobuf field is missing")

func requiredScalar(fields map[int][]wireValue, field ScalarField) (uint64, error) {
	return scalar(fields, field, true)
}

func scalar(fields map[int][]wireValue, field ScalarField, required bool) (uint64, error) {
	values := fields[field.Number]
	if len(values) == 0 {
		if required {
			return 0, errFieldMissing
		}
		return 0, errFieldMissing
	}
	if values[0].wireType != 0 {
		return 0, fmt.Errorf("field %d uses wire type %d, want varint", field.Number, values[0].wireType)
	}
	return values[0].varint ^ field.XOR, nil
}

func bytesFields(fields map[int][]wireValue, number int) [][]byte {
	values := fields[number]
	out := make([][]byte, 0, len(values))
	for _, value := range values {
		if value.wireType == 2 {
			out = append(out, value.bytes)
		}
	}
	return out
}

func decodeWireMessage(encoded []byte) (map[int][]wireValue, error) {
	fields := make(map[int][]wireValue)
	for offset := 0; offset < len(encoded); {
		key, n := binary.Uvarint(encoded[offset:])
		if n <= 0 {
			return nil, io.ErrUnexpectedEOF
		}
		offset += n
		number, wireType := int(key>>3), key&7
		if number <= 0 {
			return nil, errors.New("invalid protobuf field number 0")
		}
		value := wireValue{wireType: wireType}
		switch wireType {
		case 0:
			decoded, size := binary.Uvarint(encoded[offset:])
			if size <= 0 {
				return nil, io.ErrUnexpectedEOF
			}
			offset += size
			value.varint = decoded
		case 1:
			if len(encoded)-offset < 8 {
				return nil, io.ErrUnexpectedEOF
			}
			offset += 8
		case 2:
			length, size := binary.Uvarint(encoded[offset:])
			if size <= 0 {
				return nil, io.ErrUnexpectedEOF
			}
			offset += size
			if length > uint64(len(encoded)-offset) {
				return nil, io.ErrUnexpectedEOF
			}
			value.bytes = append([]byte(nil), encoded[offset:offset+int(length)]...)
			offset += int(length)
		case 5:
			if len(encoded)-offset < 4 {
				return nil, io.ErrUnexpectedEOF
			}
			offset += 4
		default:
			return nil, fmt.Errorf("unsupported protobuf wire type %d", wireType)
		}
		fields[number] = append(fields[number], value)
	}
	return fields, nil
}
