package cn

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestQRLoginFlowAndRedaction(t *testing.T) {
	var queryCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-rpc-game_biz") != "nap_cn" || r.Header.Get("x-rpc-device_id") == "" {
			t.Errorf("missing official SDK headers")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/nap_cn/combo/panda/qrcode/fetch":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"retcode": 0, "message": "OK",
				"data": map[string]any{"url": "https://user.mihoyo.com/qr?ticket=test-ticket&biz_key=nap_cn"},
			})
		case "/nap_cn/combo/panda/qrcode/query":
			queryCount++
			state := "Init"
			raw := ""
			if queryCount > 1 {
				state = "Confirmed"
				raw = `{"uid":"10001","token":"secret-game-token","mid":"mid-1"}`
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"retcode": 0, "message": "OK",
				"data": map[string]any{"stat": state, "payload": map[string]any{"raw": raw}},
			})
		case "/nap_cn/combo/granter/login/v2/login":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"retcode": 0, "message": "OK",
				"data": map[string]any{"open_id": "10001", "combo_token": "secret-combo-token"},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := newQRClient(server.Client(), server.URL+"/nap_cn", server.URL+"/nap_cn")
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := client.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if challenge.Ticket != "test-ticket" || challenge.DeviceID == "" {
		t.Fatalf("unexpected challenge: %#v", challenge)
	}
	if strings.Contains(challenge.String(), "test-ticket") || !strings.Contains(challenge.String(), "[REDACTED]") {
		t.Fatal("QR challenge string must redact the ticket")
	}
	waiting, err := client.Query(context.Background(), challenge)
	if err != nil || waiting.State != QRWaiting || waiting.Grant != nil {
		t.Fatalf("unexpected waiting result: %#v, %v", waiting, err)
	}
	confirmed, err := client.Query(context.Background(), challenge)
	if err != nil || confirmed.State != QRConfirmed || confirmed.Grant == nil {
		t.Fatalf("unexpected confirmed result: %#v, %v", confirmed, err)
	}
	if strings.Contains(confirmed.Grant.String(), "secret-game-token") || !strings.Contains(confirmed.Grant.String(), "[REDACTED]") {
		t.Fatal("account grant string must redact the token")
	}
	combo, err := client.ExchangeCombo(context.Background(), confirmed.Grant)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(combo.String(), "secret-combo-token") || !strings.Contains(combo.String(), "[REDACTED]") {
		t.Fatal("combo grant string must redact the token")
	}
	accountToken := confirmed.Grant.GameToken
	comboToken := combo.ComboToken
	confirmed.Grant.Destroy()
	combo.Destroy()
	for _, token := range [][]byte{accountToken, comboToken} {
		for _, value := range token {
			if value != 0 {
				t.Fatal("Destroy must zero token bytes")
			}
		}
	}
}

func TestQueryRejectsChallengeFromAnotherClient(t *testing.T) {
	client, err := NewQRClient(nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Query(context.Background(), QRChallenge{Ticket: "ticket", DeviceID: "another-device"})
	if err == nil {
		t.Fatal("expected foreign challenge to be rejected")
	}
}
