package qqnt

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image/png"
	"time"
)

type LoginQR struct {
	State     string `json:"state"`
	Status    string `json:"status"`
	PNGBase64 string `json:"png_base64,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
	LastError string `json:"last_error"`
}

func validateLoginQR(qr LoginQR) error {
	switch qr.State {
	case "booting", "waiting_login", "ready":
	default:
		return fmt.Errorf("invalid QQNT QR runtime state")
	}
	switch qr.Status {
	case "ready", "expired", "unavailable", "available":
	default:
		return fmt.Errorf("unknown QQNT QR status %q", qr.Status)
	}
	if qr.Status != "available" {
		if qr.PNGBase64 != "" {
			return fmt.Errorf("QR image provided outside available state")
		}
		return nil
	}
	if len(qr.PNGBase64) > 512*1024 {
		return fmt.Errorf("QQNT QR exceeds size limit")
	}
	data, err := base64.StdEncoding.DecodeString(qr.PNGBase64)
	if err != nil {
		return fmt.Errorf("invalid QQNT QR base64")
	}
	config, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width > 2048 || config.Height > 2048 {
		return fmt.Errorf("invalid QQNT QR PNG")
	}
	if _, err := time.Parse(time.RFC3339, qr.ExpiresAt); err != nil {
		return fmt.Errorf("invalid QQNT QR expiry")
	}
	return nil
}

// LoginStatus excludes QR data: credentials are available only on the QR API.
func (a *Adapter) LoginStatus() RuntimeInfo { return a.RuntimeInfo() }

func (a *Adapter) LoginQRCode(ctx context.Context, refresh bool) (LoginQR, error) {
	info := a.RuntimeInfo()
	if !info.Connected {
		return LoginQR{}, fmt.Errorf("QQNT runtime disconnected: %s", info.LastError)
	}
	if info.Ready {
		return LoginQR{State: info.State, Status: "ready"}, nil
	}
	a.statusMu.RLock()
	cached := a.loginQR
	a.statusMu.RUnlock()
	expiry, _ := time.Parse(time.RFC3339, cached.ExpiresAt)
	if !refresh && cached.Status == "available" && time.Now().Before(expiry) {
		cached.LastError = info.LastError
		return cached, nil
	}
	a.statusMu.Lock()
	a.loginQR = LoginQR{}
	a.statusMu.Unlock()
	result, err := a.Call(ctx, "get_login_qr", map[string]any{"refresh": refresh})
	if err != nil {
		return LoginQR{}, err
	}
	data, err := json.Marshal(result)
	if err != nil {
		return LoginQR{}, err
	}
	var qr LoginQR
	if err := json.Unmarshal(data, &qr); err != nil {
		return LoginQR{}, err
	}
	if err := validateLoginQR(qr); err != nil {
		return LoginQR{}, err
	}
	a.statusMu.Lock()
	if a.status.Connected && !a.status.Ready {
		a.loginQR = qr
	}
	a.statusMu.Unlock()
	return qr, nil
}
