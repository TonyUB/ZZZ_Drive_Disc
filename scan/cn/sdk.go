package cn

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultSDKBase     = "https://nap-sdk.mihoyo.com/nap_cn"
	defaultGameAPIBase = "https://gameapi-account.mihoyo.com/nap_cn"
	maxAPIResponse     = 1 << 20
)

type QRClient struct {
	httpClient  *http.Client
	sdkBase     string
	gameAPIBase string
	deviceID    string
}

type QRChallenge struct {
	URL      string
	Ticket   string
	DeviceID string
}

func (QRChallenge) String() string {
	return "QRChallenge{URL:[REDACTED], Ticket:[REDACTED], DeviceID:[REDACTED]}"
}

type QRState string

const (
	QRWaiting   QRState = "Init"
	QRScanned   QRState = "Scanned"
	QRConfirmed QRState = "Confirmed"
	QRExpired   QRState = "Expired"
)

type QRResult struct {
	State QRState
	Grant *AccountGrant
}

type AccountGrant struct {
	AccountUID string
	GameToken  []byte
	MID        string
}

func (g AccountGrant) String() string {
	return "AccountGrant{AccountUID:[REDACTED], GameToken:[REDACTED], MID:[REDACTED]}"
}

func (g *AccountGrant) Destroy() {
	if g == nil {
		return
	}
	for i := range g.GameToken {
		g.GameToken[i] = 0
	}
	g.AccountUID = ""
	g.GameToken = nil
	g.MID = ""
}

type ComboGrant struct {
	AccountUID string
	ComboToken []byte
	DeviceID   string
}

func (g ComboGrant) String() string {
	return "ComboGrant{AccountUID:[REDACTED], ComboToken:[REDACTED], DeviceID:[REDACTED]}"
}

func (g *ComboGrant) Destroy() {
	if g == nil {
		return
	}
	for i := range g.ComboToken {
		g.ComboToken[i] = 0
	}
	g.AccountUID = ""
	g.ComboToken = nil
	g.DeviceID = ""
}

func NewQRClient(httpClient *http.Client) (*QRClient, error) {
	return newQRClient(httpClient, defaultSDKBase, defaultGameAPIBase)
}

func newQRClient(httpClient *http.Client, sdkBase, gameAPIBase string) (*QRClient, error) {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return &QRClient{
		httpClient: httpClient, sdkBase: strings.TrimRight(sdkBase, "/"),
		gameAPIBase: strings.TrimRight(gameAPIBase, "/"), deviceID: hex.EncodeToString(b),
	}, nil
}

func (c *QRClient) Fetch(ctx context.Context) (QRChallenge, error) {
	request := struct {
		AppID  string `json:"app_id"`
		Device string `json:"device"`
	}{AppID: "12", Device: c.deviceID}
	var response apiResponse[struct {
		URL string `json:"url"`
	}]
	if err := c.post(ctx, c.sdkBase+"/combo/panda/qrcode/fetch", request, &response); err != nil {
		return QRChallenge{}, err
	}
	if err := response.check(); err != nil {
		return QRChallenge{}, err
	}
	parsed, err := url.Parse(response.Data.URL)
	if err != nil {
		return QRChallenge{}, fmt.Errorf("parse QR URL: %w", err)
	}
	ticket := parsed.Query().Get("ticket")
	if ticket == "" {
		ticket = parsed.Query().Get("tk")
	}
	if ticket == "" {
		return QRChallenge{}, errors.New("official QR response did not contain a ticket")
	}
	return QRChallenge{URL: response.Data.URL, Ticket: ticket, DeviceID: c.deviceID}, nil
}

func (c *QRClient) Query(ctx context.Context, challenge QRChallenge) (QRResult, error) {
	if challenge.DeviceID != c.deviceID || strings.TrimSpace(challenge.Ticket) == "" {
		return QRResult{}, errors.New("QR challenge does not belong to this client")
	}
	request := struct {
		AppID  string `json:"app_id"`
		Device string `json:"device"`
		Ticket string `json:"ticket"`
	}{AppID: "12", Device: c.deviceID, Ticket: challenge.Ticket}
	var response apiResponse[struct {
		State   QRState `json:"stat"`
		Payload struct {
			Raw string `json:"raw"`
		} `json:"payload"`
	}]
	if err := c.post(ctx, c.sdkBase+"/combo/panda/qrcode/query", request, &response); err != nil {
		return QRResult{}, err
	}
	if err := response.check(); err != nil {
		return QRResult{}, err
	}
	result := QRResult{State: response.Data.State}
	if response.Data.State != QRConfirmed {
		return result, nil
	}
	var raw struct {
		Token string `json:"token"`
		UID   string `json:"uid"`
		MID   string `json:"mid"`
	}
	rawBytes := []byte(response.Data.Payload.Raw)
	response.Data.Payload.Raw = ""
	defer zeroBytes(rawBytes)
	if err := json.Unmarshal(rawBytes, &raw); err != nil {
		return QRResult{}, fmt.Errorf("decode confirmed QR grant: %w", err)
	}
	if raw.UID == "" || raw.Token == "" {
		return QRResult{}, errors.New("confirmed QR response did not include an account grant")
	}
	result.Grant = &AccountGrant{AccountUID: raw.UID, GameToken: []byte(raw.Token), MID: raw.MID}
	return result, nil
}

func (c *QRClient) ExchangeCombo(ctx context.Context, account *AccountGrant) (ComboGrant, error) {
	if account == nil || account.AccountUID == "" || len(account.GameToken) == 0 {
		return ComboGrant{}, errors.New("account grant is required")
	}
	authData, err := json.Marshal(struct {
		UID   string `json:"uid"`
		Guest bool   `json:"guest"`
		Token string `json:"token"`
	}{UID: account.AccountUID, Token: string(account.GameToken)})
	if err != nil {
		return ComboGrant{}, err
	}
	defer zeroBytes(authData)
	request := struct {
		Data      string `json:"data"`
		AppID     int    `json:"app_id"`
		ChannelID int    `json:"channel_id"`
		Device    string `json:"device"`
	}{Data: string(authData), AppID: 12, ChannelID: ChannelID, Device: c.deviceID}
	var response apiResponse[struct {
		OpenID     string `json:"open_id"`
		ComboToken string `json:"combo_token"`
	}]
	if err := c.post(ctx, c.gameAPIBase+"/combo/granter/login/v2/login", request, &response); err != nil {
		return ComboGrant{}, err
	}
	if err := response.check(); err != nil {
		return ComboGrant{}, err
	}
	if response.Data.OpenID == "" || response.Data.ComboToken == "" {
		return ComboGrant{}, errors.New("combo login did not return an account token")
	}
	return ComboGrant{AccountUID: response.Data.OpenID, ComboToken: []byte(response.Data.ComboToken), DeviceID: c.deviceID}, nil
}

type apiResponse[T any] struct {
	Retcode int    `json:"retcode"`
	Message string `json:"message"`
	Data    *T     `json:"data"`
}

func (r apiResponse[T]) check() error {
	if r.Retcode != 0 {
		return fmt.Errorf("official API returned retcode %d: %s", r.Retcode, r.Message)
	}
	if r.Data == nil {
		return errors.New("official API returned no data")
	}
	return nil
}

func (c *QRClient) post(ctx context.Context, endpoint string, body, target any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return err
	}
	defer zeroBytes(encoded)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "UnityPlayer/2019.4.41f1 (UnityWebRequest/1.0)")
	request.Header.Set("X-Unity-Version", "2019.4.41f1")
	request.Header.Set("x-rpc-client_type", "3")
	request.Header.Set("x-rpc-sys_version", "Windows 10")
	request.Header.Set("x-rpc-device_id", c.deviceID)
	request.Header.Set("x-rpc-device_model", "Windows PC")
	request.Header.Set("x-rpc-device_name", "ZZZ Drive Scan")
	request.Header.Set("x-rpc-channel_id", "1")
	request.Header.Set("x-rpc-sub_channel_id", "2")
	request.Header.Set("x-rpc-language", "zh-cn")
	request.Header.Set("x-rpc-game_biz", "nap_cn")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return fmt.Errorf("official API returned HTTP %d", response.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxAPIResponse))
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode official API response: %w", err)
	}
	return nil
}

func zeroBytes(data []byte) {
	for i := range data {
		data[i] = 0
	}
}
