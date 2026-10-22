package tts

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// Edge readaloud TTS over WebSocket — the same endpoint Microsoft Edge's
// "read aloud" feature uses. Free, no API key, requires a Sec-MS-GEC token
// derived from the trusted client token plus a 5-minute-granular timestamp.
const (
	edgeTrustedClientToken = "6A5AA1D4EAFF4E9FB37E23D68491D6F4"
	edgeVoiceURL           = "wss://api.msedgeservices.com/tts/cognitiveservices/websocket/v1"
	edgeGECVersion         = "1-130.0.2849.68"
	edgeUserAgent          = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) " +
		"AppleWebKit/537.36 (KHTML, like Gecko) " +
		"Chrome/130.0.0.0 Safari/537.36 Edg/130.0.0.0"
)

type edgeProvider struct {
	voice   string
	rate    string
	pitch   string
	style   string
	timeout time.Duration
}

// Synthesize renders text to an mp3 file at outPath.
func (p *edgeProvider) Synthesize(
	ctx context.Context,
	text, outPath, voice string,
	rate float64,
) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("tts text is empty")
	}
	if p.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.timeout)
		defer cancel()
	}

	connID := randHex(16)
	wsURL := fmt.Sprintf(
		"%s?Ocp-Apim-Subscription-Key=%s&Sec-MS-GEC=%s"+
			"&Sec-MS-GEC-Version=%s&X-ConnectionId=%s",
		edgeVoiceURL,
		edgeTrustedClientToken,
		edgeSecMSGEC(),
		url.QueryEscape(edgeGECVersion),
		connID,
	)
	header := http.Header{}
	header.Set("User-Agent", edgeUserAgent)
	header.Set("Origin", "chrome-extension://jdiccldimpdaibmpdkjnbmckianbfold")
	header.Set("Pragma", "no-cache")
	header.Set("Cache-Control", "no-cache")
	header.Set("Accept-Language", "en-US,en;q=0.9")
	var (
		conn *websocket.Conn
		resp *http.Response
		err  error
	)
	// Microsoft's free endpoint occasionally rejects a perfectly valid
	// handshake with 403 — retry a few times with a short backoff.
	for attempt := 0; attempt < 3; attempt++ {
		conn, resp, err = websocket.DefaultDialer.DialContext(ctx, wsURL, header)
		if err == nil {
			break
		}
		if attempt < 2 {
			select {
			case <-ctx.Done():
			case <-time.After(time.Duration(400*(attempt+1)) * time.Millisecond):
			}
		}
	}
	if err != nil {
		status := ""
		if resp != nil {
			status = resp.Status
		}
		return fmt.Errorf("edge tts dial failed %s: %w", status, err)
	}
	defer conn.Close()

	voiceName := voice
	if voiceName == "" {
		voiceName = p.voice
	}
	ratePercent := p.rate
	if rate > 0.2 && rate <= 4.0 {
		ratePercent = fmt.Sprintf("%+.0f%%", (rate-1)*100)
	}
	if err := p.writeText(conn, speechConfigMessage()); err != nil {
		return err
	}
	if err := p.writeText(conn, ssmlMessage(connID, voiceName, p.style, ratePercent, p.pitch, text)); err != nil {
		return err
	}

	var audio []byte
	for {
		msgType, payload, readErr := conn.ReadMessage()
		if readErr != nil {
			return fmt.Errorf("edge tts read: %w", readErr)
		}
		if msgType == websocket.BinaryMessage {
			chunk := stripWSHeader(payload)
			audio = append(audio, chunk...)
			continue
		}
		if msgType != websocket.TextMessage {
			continue
		}
		body := string(payload)
		switch {
		case strings.Contains(body, "Path:turn.end"):
			if len(audio) == 0 {
				return fmt.Errorf("edge tts returned no audio")
			}
			if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
				return err
			}
			return os.WriteFile(outPath, audio, 0o600)
		case strings.Contains(body, "Path:audio.config"):
			// config acknowledgement — keep reading
		}
	}
}

func (p *edgeProvider) writeText(conn *websocket.Conn, message string) error {
	if err := conn.WriteMessage(
		websocket.TextMessage, []byte(message),
	); err != nil {
		return fmt.Errorf("edge tts write: %w", err)
	}
	return nil
}

// stripWSHeader removes the 2-byte-length-prefixed header block that Edge
// prepends to binary audio frames.
func stripWSHeader(payload []byte) []byte {
	if len(payload) < 2 {
		return payload
	}
	headerLen := int(binary.BigEndian.Uint16(payload[:2]))
	if headerLen+2 > len(payload) {
		return nil
	}
	return payload[2+headerLen:]
}

func speechConfigMessage() string {
	return "X-Timestamp:" + isoTimestamp() + "\r\n" +
		"Content-Type:application/json; charset=utf-8\r\n" +
		"Path:speech.config\r\n\r\n" +
		`{"context":{"synthesis":{"audio":{"metadataoptions":{` +
		`"sentenceBoundaryEnabled":"false","wordBoundaryEnabled":"true"},` +
		`"outputFormat":"audio-24khz-48kbitrate-mono-mp3"}}}}`
}

func ssmlMessage(connID, voice, style, rate, pitch, text string) string {
	inner := prosodyClauses(pitch, rate, text)
	if strings.TrimSpace(style) != "" {
		inner = fmt.Sprintf(
			`<mstts:express-as style='%s' styledegree='1.2'>%s</mstts:express-as>`,
			style, inner,
		)
	}
	ssml := fmt.Sprintf(
		`<speak version='1.0' xmlns='http://www.w3.org/2001/10/synthesis'`+
			` xmlns:mstts='https://www.w3.org/2001/mstts'`+
			` xml:lang='en-US'><voice name='%s'>%s</voice></speak>`,
		voice, inner,
	)
	return fmt.Sprintf(
		"X-RequestId:%s\r\nContent-Type:application/ssml+xml\r\n"+
			"X-Timestamp:%s\r\nPath:ssml\r\n\r\n%s",
		strings.ReplaceAll(connID, "-", ""),
		isoTimestamp(),
		ssml,
	)
}

// prosodyClauses splits the text into spoken clauses and emits each in
// its own prosody with a subtle alternating pitch/rate offset — one flat
// prosody for the whole line is what makes the voice sound monotone.
func prosodyClauses(pitch, rate, text string) string {
	basePitch := defaultOr(pitch, "+0Hz")
	baseRate := defaultOr(rate, "+0%")
	clauses := splitVoiceClauses(text)
	if len(clauses) <= 1 {
		return fmt.Sprintf(
			`<prosody pitch='%s' rate='%s' volume='+0%%'>%s</prosody>`,
			basePitch, baseRate, xmlEscape(text),
		)
	}
	var b strings.Builder
	for i, clause := range clauses {
		b.WriteString(fmt.Sprintf(
			`<prosody pitch='%s' rate='%s'>%s</prosody>`,
			shiftPitch(basePitch, i), shiftRate(baseRate, i),
			xmlEscape(clause),
		))
	}
	return b.String()
}

// splitVoiceClauses cuts at terminal punctuation and at mid pauses once
// a clause runs long, so breathing rhythm lands at natural seams.
func splitVoiceClauses(text string) []string {
	var clauses []string
	runes := []rune(text)
	start := 0
	for i, r := range runes {
		terminal := strings.ContainsRune("。！？?!；;\n", r)
		midPause := strings.ContainsRune("，,、：:~～…", r)
		if terminal || (midPause && i-start >= 18) {
			if seg := strings.TrimSpace(string(runes[start : i+1])); seg != "" {
				clauses = append(clauses, seg)
			}
			start = i + 1
		}
	}
	if seg := strings.TrimSpace(string(runes[start:])); seg != "" {
		clauses = append(clauses, seg)
	}
	return clauses
}

var clausePitchDrift = []int{0, 9, -5, 4}
var clauseRateDrift = []int{0, 4, -4, 2}

func shiftValue(base string, drift int, unit string) string {
	trimmed := strings.TrimSuffix(strings.TrimSpace(base), unit)
	value, err := strconv.Atoi(trimmed)
	if err != nil {
		return base
	}
	return fmt.Sprintf("%+d%s", value+drift, unit)
}

func shiftPitch(base string, idx int) string {
	return shiftValue(base, clausePitchDrift[idx%len(clausePitchDrift)], "Hz")
}

func shiftRate(base string, idx int) string {
	return shiftValue(base, clauseRateDrift[idx%len(clauseRateDrift)], "%")
}

// edgeSecMSGEC derives the DRM token: uppercase SHA-256 of the Windows-filetime
// timestamp truncated to 5 minutes, concatenated with the client token.
func edgeSecMSGEC() string {
	const fileTimeEpochOffset = 11644473600 // seconds 1601→1970
	now := time.Now().UTC().Unix()
	ticks := (now + fileTimeEpochOffset) * 10_000_000
	ticks -= ticks % 3_000_000_000 // truncate to 5-minute boundary
	sum := sha256.Sum256(
		[]byte(fmt.Sprintf("%d%s", ticks, edgeTrustedClientToken)),
	)
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

func isoTimestamp() string {
	return time.Now().UTC().Format(
		"Mon Jan 02 2006 15:04:05 GMT+0000 (Coordinated Universal Time)",
	)
}

func randHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return strings.Repeat("a", n*2)
	}
	return hex.EncodeToString(buf)
}

func defaultOr(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

var xmlEscaper = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
	"'", "&apos;",
	`"`, "&quot;",
)

func xmlEscape(text string) string {
	return xmlEscaper.Replace(text)
}
