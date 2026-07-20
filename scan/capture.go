package scan

import (
	"context"
	"errors"
	"time"
)

// Source is implemented by a user-authorized, version-specific login/session
// provider. Implementations return the complete GetEquipData response and must
// discard authentication material before returning.
type Source interface {
	FetchAll(context.Context) (EquipmentResponse, error)
}

// AuthorizedSession is the narrow boundary implemented by a region adapter.
// RoundTrip receives command ids and an encoded protobuf body; it owns QR
// authorization, transport encryption and response decryption. Implementations
// must verify the response command and must not log payloads or credentials.
type AuthorizedSession interface {
	RoundTrip(ctx context.Context, requestCommand, responseCommand uint16, requestBody []byte) ([]byte, error)
}

// ActiveSource requests the complete equipment list from a user-authorized
// session. GetEquipDataCsReq currently has an empty protobuf body; the command
// ids and response layout come from the exact-version Adapter.
type ActiveSource struct {
	Session       AuthorizedSession
	Adapter       Adapter
	ClientVersion string
}

func (s ActiveSource) FetchAll(ctx context.Context) (EquipmentResponse, error) {
	if s.Session == nil {
		return EquipmentResponse{}, errors.New("authorized session is required")
	}
	if err := s.Adapter.Validate(s.ClientVersion); err != nil {
		return EquipmentResponse{}, err
	}
	body, err := s.Session.RoundTrip(ctx, s.Adapter.Command.GetEquipDataRequest, s.Adapter.Command.GetEquipDataResponse, nil)
	if err != nil {
		return EquipmentResponse{}, err
	}
	return DecodeEquipmentResponse(body, s.Adapter, s.ClientVersion)
}

type CaptureOptions struct {
	Adapter       Adapter
	Catalog       Catalog
	ClientVersion string
	AccountUID    string
	Now           func() time.Time
}

func Capture(ctx context.Context, source Source, options CaptureOptions) (Export, error) {
	if source == nil {
		return Export{}, errors.New("capture source is required")
	}
	if err := options.Adapter.Validate(options.ClientVersion); err != nil {
		return Export{}, err
	}
	if err := options.Catalog.Validate(options.Adapter.ID); err != nil {
		return Export{}, err
	}
	response, err := source.FetchAll(ctx)
	if err != nil {
		return Export{}, err
	}
	now := time.Now().UTC()
	if options.Now != nil {
		now = options.Now().UTC()
	}
	return BuildExport(response, options.Adapter, options.Catalog, options.ClientVersion, options.AccountUID, now)
}

type StaticSource struct {
	Response EquipmentResponse
	Err      error
}

func (s StaticSource) FetchAll(context.Context) (EquipmentResponse, error) {
	return s.Response, s.Err
}
