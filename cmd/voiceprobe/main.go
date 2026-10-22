// voiceprobe synthesizes sample lines through the configured TTS
// provider so seed-tts voices can be auditioned without deploying.
//
// Usage (PowerShell):
//
//	$env:VOICE_MODE="volcano"; $env:VOICE_VOLC_TOKEN="ark-..."
//	go run ./cmd/voiceprobe -speakers a,b -text "试音文本" -tag linjia
//
// Each speaker writes data/voice-probe/<tag>_<speaker>.mp3 and prints
// OK or the API error. Unknown speaker ids fail clean — cheap way to
// discover which voices the account's resource id actually supports.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/tts"
)

func env(key string) string { return strings.TrimSpace(os.Getenv(key)) }

func main() {
	speakersFlag := flag.String("speakers", "", "comma-separated speaker ids")
	textFlag := flag.String("text", "", "sample line to synthesize")
	tagFlag := flag.String("tag", "", "output filename prefix")
	outDirFlag := flag.String("out", "", "output dir (default data/voice-probe)")
	emotionFlag := flag.String("emotion", "", "override VOICE_VOLC_EMOTION")
	flag.Parse()

	// Voice env only — the probe must run without full bot config.
	volcEmotion := env("VOICE_VOLC_EMOTION")
	if e := strings.TrimSpace(*emotionFlag); e != "" {
		volcEmotion = e
	}

	text := strings.TrimSpace(*textFlag)
	if text == "" {
		text = "哼，才、才不是特意等你的呢……好吧，既然你来了，本小姐就勉为其难陪你聊两句。"
	}
	outDir := strings.TrimSpace(*outDirFlag)
	if outDir == "" {
		outDir = "data/voice-probe"
	}
	tag := strings.TrimSpace(*tagFlag)
	if tag != "" {
		tag += "_"
	}
	speakers := []string{
		// confirmed seed-tts-2.0 ids (docs/ListSpeakers examples)
		"zh_female_vv_uranus_bigtts",
		"zh_female_xiaohe_uranus_bigtts",
		"zh_female_tianmeitaozi_mars_bigtts",
		// plausible 2.0 names to probe — failures are reported, not fatal
		"zh_female_linjianvhai_uranus_bigtts",
		"zh_female_yuanqishaonv_uranus_bigtts",
		"zh_female_tianmeixiaoyuan_uranus_bigtts",
		"zh_female_qingxinshaonv_uranus_bigtts",
		"zh_female_peiqi_uranus_bigtts",
	}
	if *speakersFlag != "" {
		speakers = strings.Split(*speakersFlag, ",")
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "mkdir: %v\n", err)
		os.Exit(1)
	}
	for _, speaker := range speakers {
		speaker = strings.TrimSpace(speaker)
		if speaker == "" {
			continue
		}
		provider, err := tts.New(tts.Config{
			Mode:           env("VOICE_MODE"),
			Voice:          speaker,
			APIURL:         env("VOICE_API_URL"),
			VolcAppID:      env("VOICE_VOLC_APP_ID"),
			VolcToken:      env("VOICE_VOLC_TOKEN"),
			VolcResourceID: env("VOICE_VOLC_RESOURCE_ID"),
			VolcEmotion:    volcEmotion,
			Timeout:        15 * time.Second,
		})
		if err != nil {
			fmt.Printf("%-46s INIT FAIL: %v\n", speaker, err)
			continue
		}
		if provider == nil {
			fmt.Println("provider disabled — set VOICE_MODE=volcano")
			os.Exit(1)
		}
		outPath := filepath.Join(outDir, tag+speaker+".mp3")
		start := time.Now()
		err = provider.Synthesize(context.Background(), text, outPath, "", 0)
		if err != nil {
			fmt.Printf("%-46s FAIL: %v\n", speaker, err)
			continue
		}
		info, _ := os.Stat(outPath)
		fmt.Printf(
			"%-46s OK  %6.1fKB  %v  -> %s\n",
			speaker, float64(info.Size())/1024,
			time.Since(start).Round(time.Millisecond), outPath,
		)
	}
}
