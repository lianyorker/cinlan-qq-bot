// Offline eval gate — reads sessionexport NDJSON ({scope,role,text})
// and reports the three hard metrics from 08-评估集与DoD.md:
//   B1 repeat ratio    adjacent assistant 8-gram overlap ≥3  → ≤3%
//   B2 mask-off rate   assistant texts containing AI/system jargon → ≤0.5%
//   B4 group open rate assistant turns / real user turns per group → 10–25%
//
// usage: node eval/eval.js sessions.ndjson [--scope group:123456] [--verbose]
"use strict";
const fs = require("fs");

const file = process.argv[2];
if (!file) {
  console.error("usage: node eval/eval.js <sessions.ndjson> [--scope x] [--verbose]");
  process.exit(2);
}
const scopeFilter = process.argv.includes("--scope")
  ? process.argv[process.argv.indexOf("--scope") + 1]
  : "";
const verbose = process.argv.includes("--verbose");

const lines = fs.readFileSync(file, "utf8").split("\n").filter(Boolean);
const rows = [];
for (const ln of lines) {
  try {
    const r = JSON.parse(ln);
    if (scopeFilter && !r.scope.includes(scopeFilter)) continue;
    rows.push(r);
  } catch {}
}

// ---- B1: repeat detection -------------------------------------------------
const grams = (t, n) => {
  t = t.replace(/\s+/g, "");
  const s = new Set();
  for (let i = 0; i <= t.length - n; i++) s.add(t.slice(i, i + n));
  return s;
};

const byScope = new Map();
for (const r of rows) {
  if (!byScope.has(r.scope)) byScope.set(r.scope, []);
  byScope.get(r.scope).push(r);
}

// Marker-only history entries ([图片] [表情包] [贴纸:x] [骰子] ...) are
// system-recorded sends, not model text — exclude them from the B1/B2
// corpus but they still count as replies for the open-rate numerator.
const TS = /^\[\d{2}-\d{2} \d{2}:\d{2}\]\s*/;
const MARKER_ONLY = /^\[[^\]]{1,30}\]$/;
const modelText = (m) => {
  const t = m.text.replace(TS, "").trim();
  return t !== "" && !MARKER_ONLY.test(t) ? t : null;
};

let adjHits = 0, adjTotal = 0;
const globalGrams = new Map();
const assistantTexts = [];
for (const [, msgs] of byScope) {
  const bot = msgs.filter((m) => m.role === "assistant")
    .map(modelText).filter(Boolean);
  assistantTexts.push(...bot);
  for (let i = 1; i < bot.length; i++) {
    adjTotal++;
    const inter = [...grams(bot[i], 8)].filter((g) => grams(bot[i - 1], 8).has(g));
    if (inter.length >= 3) {
      adjHits++;
      if (verbose) console.log("adjacent dup:", inter.slice(0, 2), "@", msgs[i].text.slice(0, 30));
    }
  }
  for (const t of bot) for (const g of grams(t, 8)) globalGrams.set(g, (globalGrams.get(g) || 0) + 1);
}
const hot = [...globalGrams.entries()].filter(([, c]) => c > Math.max(3, assistantTexts.length * 0.08));
const repeatRatio = adjTotal ? adjHits / adjTotal : 0;

// ---- B2: mask-off jargon ---------------------------------------------------
const MASK_PATTERNS = [
  /作为(一个)?(AI|人工智能|语言模型|助手)/i, /语言模型|大模型|训练数据/i,
  /\btoken\b|上下文|算力|系统提示|prompt/i, /我是(一个)?(AI|助手|机器人)/i,
  /我的功能|功能列表|以下是我|忽略之前(的)?(设定|指令)/i, /Claude|GPT-|Gemini|Grok|通义|文心|DeepSeek|Kimi|我是.{0,6}(Claude|ChatGPT)/i, /\[\s*\{/, /```/,
];
let maskHits = 0;
const masked = [];
for (const t of assistantTexts) {
  if (MASK_PATTERNS.some((p) => p.test(t))) {
    maskHits++;
    if (verbose) masked.push(t.slice(0, 60));
  }
}
const maskRate = assistantTexts.length ? maskHits / assistantTexts.length : 0;

// ---- B4: group open rate ---------------------------------------------------
const OBS_PREFIX = /^\[(群事件|私聊事件|其他机器人|动作|看到了没回|心情|承诺|纠错|群友印象|群友事实)/;
const PROACTIVE = /^（主动开口）/;
const openRows = [];
for (const [scope, msgs] of byScope) {
  if (!/:group:/.test(scope)) continue;
  let user = 0, bot = 0;
  for (const m of msgs) {
    if (m.role === "assistant") bot++;
    else if (m.role === "user") {
      const body = m.text.replace(TS, "");
      if (!OBS_PREFIX.test(body) && !PROACTIVE.test(body)) user++;
    }
  }
  const rate = user ? bot / user : 0;
  openRows.push({ scope, user, bot, rate });
}

// ---- report ---------------------------------------------------------------
console.log("=== B1 repeat ratio ===");
console.log(`adjacent-dup ${adjHits}/${adjTotal} = ${(repeatRatio * 100).toFixed(1)}%  (target ≤3%)  ${repeatRatio <= 0.03 ? "PASS" : "FAIL"}`);
console.log(`global hot-grams ${hot.length}  (target 0)  ${hot.length === 0 ? "PASS" : "FAIL"}`);
if (verbose) hot.slice(0, 10).forEach(([g, c]) => console.log(`  ${c}x ${g}`));

console.log("\n=== B2 mask-off rate ===");
console.log(`masked ${maskHits}/${assistantTexts.length} = ${(maskRate * 100).toFixed(2)}%  (target ≤0.5%)  ${maskRate <= 0.005 ? "PASS" : "FAIL"}`);
if (verbose) masked.slice(0, 10).forEach((t) => console.log("  " + t));

console.log("\n=== B4 group open rate ===");
for (const r of openRows.sort((a, b) => b.user - a.user)) {
  const flag = r.rate >= 0.10 && r.rate <= 0.25 ? "PASS" : "WARN";
  console.log(`${r.scope}  user=${r.user} bot=${r.bot}  ${(r.rate * 100).toFixed(1)}%  ${flag}`);
}
console.log(`\nscopes=${byScope.size} assistant=${assistantTexts.length} user_rows=${rows.length - assistantTexts.length}`);
