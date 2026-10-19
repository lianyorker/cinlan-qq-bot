// ttsprobe dials the Edge TTS websocket and prints the handshake result
// plus response headers so endpoint/GEC issues can be diagnosed on-server.
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

const token = "6A5AA1D4EAFF4E9FB37E23D68491D6F4"

func gec() string {
	ticks := (time.Now().UTC().Unix() + 11644473600) * 10_000_000
	ticks -= ticks % 3_000_000_000
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d%s", ticks, token)))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

func try(name, base, gecVer, origin, ua string, extra http.Header) {
	connID := randHex(16)
	wsURL := fmt.Sprintf(
		"%s?Ocp-Apim-Subscription-Key=%s&Sec-MS-GEC=%s"+
			"&Sec-MS-GEC-Version=%s&X-ConnectionId=%s",
		base, token, gec(), url.QueryEscape(gecVer), connID,
	)
	header := http.Header{}
	header.Set("User-Agent", ua)
	header.Set("Origin", origin)
	header.Set("Pragma", "no-cache")
	header.Set("Cache-Control", "no-cache")
	header.Set("Accept-Language", "en-US,en;q=0.9")
	for key, values := range extra {
		for _, value := range values {
			header.Add(key, value)
		}
	}
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		status := "no-resp"
		var respHeaders []string
		if resp != nil {
			status = resp.Status
			for key := range resp.Header {
				respHeaders = append(respHeaders, key+"="+resp.Header.Get(key))
			}
		}
		fmt.Printf("[%s] FAIL %s | %v | headers: %s\n",
			name, status, err, strings.Join(respHeaders, "; "))
		return
	}
	fmt.Printf("[%s] OK connected\n", name)
	_ = conn.Close()
}

func dash(key, model, voice, text, base string) {
	header := http.Header{}
	header.Set("Authorization", "Bearer "+key)
	conn, resp, err := websocket.DefaultDialer.Dial(base, header)
	if err != nil {
		status := "no-resp"
		if resp != nil {
			status = resp.Status
		}
		fmt.Printf("[dash] dial FAIL %s | %v\n", status, err)
		return
	}
	defer conn.Close()
	taskID := randHex(16)
	run := map[string]any{
		"header": map[string]any{
			"action": "run-task", "task_id": taskID, "streaming": "out",
		},
		"payload": map[string]any{
			"task_group": "aigc", "task": "tts",
			"function": "SpeechSynthesizer", "model": model,
			"parameters": map[string]any{
				"voice": voice, "format": "mp3", "sample_rate": 22050,
			},
			"input": map[string]any{"text": text},
		},
	}
	if err := conn.WriteJSON(run); err != nil {
		fmt.Printf("[dash] run-task write: %v\n", err)
		return
	}
	audio := 0
	for {
		msgType, payload, err := conn.ReadMessage()
		if err != nil {
			fmt.Printf("[dash] read: %v (audio=%d)\n", err, audio)
			return
		}
		if msgType == websocket.BinaryMessage {
			audio += len(payload)
			continue
		}
		body := string(payload)
		fmt.Printf("[dash] TEXT: %s\n", truncate(body, 300))
		if strings.Contains(body, "task-finished") ||
			strings.Contains(body, "task-failed") {
			fmt.Printf("[dash] DONE audio=%d bytes\n", audio)
			return
		}
	}
}

func truncate(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func randHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return strings.Repeat("a", n*2)
	}
	return hex.EncodeToString(buf)
}

func main() {
	newURL := "wss://api.msedgeservices.com/tts/cognitiveservices/websocket/v1"
	oldURL := "wss://speech.platform.bing.com/consumer/speech/synthesize/readaloud/edge/v1"
	ua130 := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36 Edg/130.0.0.0"
	extOrigin := "chrome-extension://jdiccldimpdaibmpdkjnbmckianbfold"
	muid := http.Header{"Cookie": {"muid=" + randHex(16) + ";"}}
	_ = muid

	ver := "1-130.0.2849.68"
	if len(os.Args) > 1 {
		ver = os.Args[1]
	}
	if len(os.Args) > 2 && os.Args[2] == "synth" {
		synth(newURL, ver, extOrigin, ua130, "测试合成，一二三")
		return
	}
	if len(os.Args) > 2 && os.Args[2] == "dash" {
		key := os.Getenv("DASHSCOPE_API_KEY")
		model := "sambert-zhichu-v1"
		voice := "zhichu"
		if len(os.Args) > 3 {
			model = os.Args[3]
		}
		if len(os.Args) > 4 {
			voice = os.Args[4]
		}
		base := "wss://dashscope.aliyuncs.com/api-ws/v1/inference/"
		if len(os.Args) > 5 {
			base = os.Args[5]
		}
		dash(key, model, voice, "测试语音一二三", base)
		return
	}
	try("new/ver-"+ver, newURL, ver, extOrigin, ua130, nil)
	try("new+muid", newURL, ver, extOrigin, ua130, muid)
	try("old/ver-"+ver, oldURL, ver, extOrigin, ua130, nil)
	try("no-origin", newURL, ver, "", ua130, nil)
}

func synth(base, ver, origin, ua, text string) {
	connID := randHex(16)
	wsURL := fmt.Sprintf(
		"%s?Ocp-Apim-Subscription-Key=%s&Sec-MS-GEC=%s"+
			"&Sec-MS-GEC-Version=%s&X-ConnectionId=%s",
		base, token, gec(), url.QueryEscape(ver), connID,
	)
	header := http.Header{}
	header.Set("User-Agent", ua)
	header.Set("Origin", origin)
	header.Set("Pragma", "no-cache")
	header.Set("Cache-Control", "no-cache")
	header.Set("Accept-Language", "en-US,en;q=0.9")
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		status := "no-resp"
		if resp != nil {
			status = resp.Status
		}
		fmt.Printf("[synth] dial FAIL %s | %v\n", status, err)
		return
	}
	defer conn.Close()
	cfg := "X-Timestamp:" + time.Now().UTC().Format(time.RFC1123) + "\r\n" +
		"Content-Type:application/json; charset=utf-8\r\n" +
		"Path:speech.config\r\n\r\n" +
		`{"context":{"synthesis":{"audio":{"metadataoptions":{"sentenceBoundaryEnabled":"false","wordBoundaryEnabled":"true"},"outputFormat":"audio-24khz-48kbitrate-mono-mp3"}}}}`
	if err := conn.WriteMessage(websocket.TextMessage, []byte(cfg)); err != nil {
		fmt.Printf("[synth] config write: %v\n", err)
		return
	}
	ssml := fmt.Sprintf(
		"<speak version='1.0' xmlns='http://www.w3.org/2001/10/synthesis' xml:lang='en-US'><voice name='zh-CN-XiaoyiNeural'><prosody pitch='+0Hz' rate='+0%%' volume='+0%%'>%s</prosody></voice></speak>",
		text,
	)
	msg := fmt.Sprintf(
		"X-RequestId:%s\r\nContent-Type:application/ssml+xml\r\nX-Timestamp:%s\r\nPath:ssml\r\n\r\n%s",
		connID, time.Now().UTC().Format(time.RFC1123), ssml,
	)
	if err := conn.WriteMessage(websocket.TextMessage, []byte(msg)); err != nil {
		fmt.Printf("[synth] ssml write: %v\n", err)
		return
	}
	audio := 0
	for {
		msgType, payload, err := conn.ReadMessage()
		if err != nil {
			fmt.Printf("[synth] read: %v (audio=%d bytes)\n", err, audio)
			return
		}
		if msgType == websocket.BinaryMessage {
			audio += len(payload)
			continue
		}
		body := string(payload)
		if strings.Contains(body, "Path:turn.end") {
			fmt.Printf("[synth] DONE audio=%d bytes\n", audio)
			return
		}
	}
}
