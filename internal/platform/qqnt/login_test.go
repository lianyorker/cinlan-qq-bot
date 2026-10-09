package qqnt

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"
)

const fixtureQR = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aS9sAAAAASUVORK5CYII="

func TestLoginQRIPCAndReadiness(t *testing.T) {
	a, err := NewAdapter(Config{Token: "fixture-token"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		c, e := l.Accept()
		if e != nil {
			done <- e
			return
		}
		done <- a.serveConnection(ctx, c)
	}()
	c, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(time.Second))
	scanner := bufio.NewScanner(c)
	writeTestEnvelope(t, c, envelope{Type: "hello", Token: "fixture-token", Payload: mustJSON(t, helloPayload{Runtime: "cinlan-qqnt"})})
	scanTestEnvelope(t, scanner)
	qr := LoginQR{State: "waiting_login", Status: "available", PNGBase64: fixtureQR, ExpiresAt: time.Now().Add(time.Minute).Format(time.RFC3339)}
	writeTestEnvelope(t, c, envelope{Type: "login_qr", Payload: mustJSON(t, qr)})
	writeTestEnvelope(t, c, envelope{Type: "runtime_status", Payload: mustJSON(t, runtimeStatus{State: "waiting_login", WrapperLoaded: true})})
	eventually(t, time.Second, func() bool { return a.RuntimeInfo().State == "waiting_login" })
	if a.Connected() {
		t.Fatal("QR availability claimed readiness")
	}
	got, err := a.LoginQRCode(ctx, false)
	if err != nil || got.PNGBase64 != fixtureQR {
		t.Fatalf("cached QR: %#v, %v", got, err)
	}
	select {
	case event := <-a.Events():
		t.Fatalf("QR leaked into pipeline: %#v", event)
	default:
	}
	results := make(chan LoginQR, 1)
	errors := make(chan error, 1)
	go func() { q, e := a.LoginQRCode(ctx, true); results <- q; errors <- e }()
	action := scanTestEnvelope(t, scanner)
	var p actionPayload
	_ = decodePayload(action.Payload, &p)
	if p.Name != "get_login_qr" || p.Params["refresh"] != true {
		t.Fatalf("refresh action: %#v", p)
	}
	writeTestEnvelope(t, c, envelope{Type: "action_result", ID: action.ID, OK: true, Payload: mustJSON(t, qr)})
	if q := <-results; q.PNGBase64 != fixtureQR {
		t.Fatal(q)
	}
	if e := <-errors; e != nil {
		t.Fatal(e)
	}
	writeTestEnvelope(t, c, envelope{Type: "runtime_status", Payload: mustJSON(t, runtimeStatus{State: "ready", SelfID: "10001", WrapperLoaded: true, SessionAttached: true})})
	eventually(t, time.Second, a.Connected)
	got, err = a.LoginQRCode(ctx, false)
	if err != nil || got.Status != "ready" || got.PNGBase64 != "" {
		t.Fatalf("ready QR: %#v, %v", got, err)
	}
	c.Close()
	<-done
	if _, err = a.LoginQRCode(ctx, false); err == nil {
		t.Fatal("QR returned on disconnected runtime")
	}
}

func TestInvalidLoginQRRejected(t *testing.T) {
	for _, qr := range []LoginQR{
		{State: "waiting_login", Status: "available", PNGBase64: "not-png"},
		{State: "waiting_login", Status: "ready", PNGBase64: fixtureQR},
		{State: "waiting_login", Status: "available", PNGBase64: fixtureQR, ExpiresAt: "invalid"},
		{State: "unknown", Status: "available", PNGBase64: fixtureQR},
	} {
		if validateLoginQR(qr) == nil {
			t.Fatalf("accepted invalid QR: %#v", qr)
		}
	}
}
