package scan

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	FrameHeadMagic uint32 = 0x01234567
	FrameTailMagic uint32 = 0x89ABCDEF
	maxFrameSize          = 16 << 20
)

type Frame struct {
	CommandID uint16
	Header    []byte
	Body      []byte
}

// DecodeFrame validates the standard game command envelope. The body must be
// decrypted by an authorized active session before it is passed to
// DecodeEquipmentResponse; passive capture alone cannot derive that key.
func DecodeFrame(encoded []byte) (Frame, error) {
	if len(encoded) < 16 {
		return Frame{}, errors.New("frame is shorter than the 16-byte envelope")
	}
	if binary.BigEndian.Uint32(encoded[:4]) != FrameHeadMagic {
		return Frame{}, errors.New("invalid frame head magic")
	}
	headerLength := int(binary.BigEndian.Uint16(encoded[6:8]))
	bodyLength := int(binary.BigEndian.Uint32(encoded[8:12]))
	if headerLength < 0 || bodyLength < 0 || headerLength+bodyLength > maxFrameSize {
		return Frame{}, errors.New("frame length exceeds safety limit")
	}
	want := 12 + headerLength + bodyLength + 4
	if len(encoded) != want {
		return Frame{}, fmt.Errorf("frame length mismatch: got %d, want %d", len(encoded), want)
	}
	if binary.BigEndian.Uint32(encoded[want-4:]) != FrameTailMagic {
		return Frame{}, errors.New("invalid frame tail magic")
	}
	headerEnd := 12 + headerLength
	return Frame{
		CommandID: binary.BigEndian.Uint16(encoded[4:6]),
		Header:    append([]byte(nil), encoded[12:headerEnd]...),
		Body:      append([]byte(nil), encoded[headerEnd:headerEnd+bodyLength]...),
	}, nil
}
