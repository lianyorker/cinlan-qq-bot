#!/usr/bin/env python3
"""DashScope TTS synthesis → mp3 file.

Usage: tts_synth.py "<text>" "<output.mp3>"
Env:   DASHSCOPE_API_KEY (required), TTS_MODEL, TTS_VOICE
"""
import os
import sys

import dashscope
from dashscope.audio.tts_v2 import SpeechSynthesizer


def main() -> int:
    if len(sys.argv) < 3:
        print("usage: tts_synth.py <text> <output.mp3>", file=sys.stderr)
        return 2
    text, out = sys.argv[1], sys.argv[2]
    if not text.strip():
        print("empty text", file=sys.stderr)
        return 2

    dashscope.api_key = os.environ["DASHSCOPE_API_KEY"]
    model = os.environ.get("TTS_MODEL", "sambert-zhichu-v1")
    voice = os.environ.get("TTS_VOICE", "zhichu")

    synthesizer = SpeechSynthesizer(
        model=model,
        voice=voice,
        format="mp3",
        speech_rate=1.0,
    )
    audio = synthesizer.call(text)
    if not audio:
        print("synthesizer returned no audio", file=sys.stderr)
        return 1
    with open(out, "wb") as handle:
        handle.write(audio)
    return 0


if __name__ == "__main__":
    sys.exit(main())
