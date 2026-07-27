package qqnt

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestProtocolRejectsOversizedFrame(t *testing.T) {
	_, err := encodeEnvelope(envelope{
		Type:    "event",
		Payload: json.RawMessage(`{"text":"` + strings.Repeat("x", 128) + `"}`),
	}, 64)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("encodeEnvelope() error = %v", err)
	}
}

func TestProtocolRejectsUnknownVersion(t *testing.T) {
	_, err := decodeEnvelope([]byte(`{"v":2,"type":"hello"}`))
	if err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("decodeEnvelope() error = %v", err)
	}
}
