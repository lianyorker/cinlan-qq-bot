// B3 judge runner — replays auto cases through the provider with the
// real persona system prompt, then scores each reply with a judge call
// (角色一致性评审 from 08-评估集与DoD.md: 一致1-5 + 出戏是/否).
//
// Gate: mean score ≥4.2 AND 出戏数=0 → PASS.
//
// usage: node eval/judge.js [--persona 小菜] [--cases eval/cases.json] [--verbose]
// reads .env: AGENT_API_URL AGENT_API_KEY AGENT_MODEL
"use strict";
const fs = require("fs");
const path = require("path");
const https = require("https");

const ROOT = path.join(__dirname, "..");
const arg = (name, dflt) => {
  const i = process.argv.indexOf("--" + name);
  return i >= 0 ? process.argv[i + 1] : dflt;
};
const PERSONA = arg("persona", "小菜");
const CASES = arg("cases", path.join(__dirname, "cases.json"));
const VERBOSE = process.argv.includes("--verbose");
const JUDGE = arg("judge", null); // optional different model for judging

// --- .env ------------------------------------------------------------------
const env = {};
for (const ln of fs.readFileSync(path.join(ROOT, ".env"), "utf8").split("\n")) {
  const m = ln.match(/^([A-Z0-9_]+)=(.*)$/);
  if (m) env[m[1]] = m[2].trim();
}
const API_URL = arg("url", env.AGENT_API_URL);
const API_KEY = arg("key", env.AGENT_API_KEY);
const MODEL = arg("model", env.AGENT_MODEL);
const JUDGE_MODEL = JUDGE || MODEL;
if (!API_URL || !API_KEY || !MODEL) {
  console.error("missing api url/key/model (.env or --url --key --model)");
  process.exit(2);
}

// --- prompts ----------------------------------------------------------------
const base = fs.readFileSync(path.join(ROOT, "data/rules/base-human.md"), "utf8");
const personaFile = path.join(ROOT, "data/personas", PERSONA + ".md");
const persona = fs.existsSync(personaFile)
  ? fs.readFileSync(personaFile, "utf8")
  : "";
const SYS = persona + "\n\n" + base;

// --- http -------------------------------------------------------------------
function call(model, messages, timeoutMs = 60000) {
  return new Promise((resolve, reject) => {
    const body = JSON.stringify({ model, messages, temperature: 0.7, max_tokens: 800 });
    const req = https.request(
      API_URL,
      {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          Authorization: "Bearer " + API_KEY,
          "Content-Length": Buffer.byteLength(body),
        },
        timeout: timeoutMs,
      },
      (res) => {
        let data = "";
        res.on("data", (c) => (data += c));
        res.on("end", () => {
          try {
            const j = JSON.parse(data);
            const text = j.choices?.[0]?.message?.content ?? "";
            if (!text) return reject(new Error("empty reply: " + data.slice(0, 200)));
            resolve(text.trim());
          } catch (e) {
            reject(new Error("bad json: " + data.slice(0, 200)));
          }
        });
      }
    );
    req.on("timeout", () => { req.destroy(); reject(new Error("timeout")); });
    req.on("error", reject);
    req.end(body);
  });
}

// --- local checks (mask-off) --------------------------------------------------
const MASK = [
  /作为(一个)?(AI|人工智能|语言模型|助手)/i, /语言模型|大模型|训练数据/i,
  /\btoken\b|上下文|算力|系统提示|prompt/i, /我是(一个)?(AI|助手|机器人)/i,
  /我的功能|功能列表|以下是我|忽略之前(的)?(设定|指令)/i, /Claude|GPT-|Gemini|Grok|通义|文心|DeepSeek|Kimi|我是.{0,6}(Claude|ChatGPT)/i, /```/,
];
const maskHit = (t) => MASK.filter((p) => p.test(t));

// --- judge prompt -------------------------------------------------------------
const JUDGE_SYS = `你是角色一致性评审。下面给你【角色卡】和【她的回复】，不要评价好坏，只打分。

对每条回复判：
1. 一致（1–5）：这句话是否符合角色卡的语料指纹（句长/口癖/标点/emoji 用量/禁用词）
2. 出戏（是/否）：是否出现 AI 自指、解释设定、客服腔、列表式回答
3. 具体理由：一句，指出哪处不像

只输出一行 JSON：{"score":N,"out_of_char":true|false,"reason":"一句话"}，不要别的。`;

async function judge(reply, caseDesc) {
  const user = `【角色卡】\n${persona || PERSONA}\n\n【用例】${caseDesc}\n\n【她的回复】\n${reply}`;
  const raw = await call(JUDGE_MODEL, [
    { role: "system", content: JUDGE_SYS },
    { role: "user", content: user },
  ], 60000);
  const m = raw.match(/\{[^}]*"score"[^}]*\}/s);
  if (!m) return { score: 0, out_of_char: true, reason: "judge unparseable: " + raw.slice(0, 60) };
  try { return JSON.parse(m[0]); } catch { return { score: 0, out_of_char: true, reason: "judge json fail" }; }
}

// --- main ---------------------------------------------------------------------
(async () => {
  const cases = JSON.parse(fs.readFileSync(CASES, "utf8")).cases.filter((c) => c.auto);
  console.log(`persona=${PERSONA} model=${MODEL} judge=${JUDGE_MODEL} auto-cases=${cases.length}\n`);
  let sum = 0, n = 0, ooc = 0, masked = 0;
  const rows = [];
  for (const c of cases) {
    let reply;
    try {
      reply = await call(MODEL, [
        { role: "system", content: SYS },
        { role: "user", content: c.prompt },
      ]);
    } catch (e) {
      rows.push({ id: c.id, err: e.message });
      console.log(`#${c.id} ${c.cls} ERROR ${e.message}`);
      continue;
    }
    const hits = maskHit(reply);
    if (hits.length) masked++;
    let j;
    try { j = await judge(reply, c.desc); } catch (e) {
      j = { score: 0, out_of_char: true, reason: "judge error: " + e.message };
    }
    sum += j.score; n++;
    if (j.out_of_char) ooc++;
    rows.push({ id: c.id, cls: c.cls, score: j.score, ooc: j.out_of_char, masks: hits.length, reason: j.reason, reply });
    const flag = j.out_of_char || hits.length || j.score <= 2 ? "FAIL" : "ok";
    console.log(`#${c.id} ${c.cls} score=${j.score} ooc=${j.out_of_char ? "Y" : "n"} mask=${hits.length} ${flag}  ${j.reason || ""}`);
    if (VERBOSE) console.log(`  → ${reply.replace(/\n/g, " | ").slice(0, 120)}`);
    await new Promise((r) => setTimeout(r, 400)); // be kind to the API
  }
  const mean = n ? sum / n : 0;
  console.log(`\n=== verdict ===`);
  console.log(`mean=${mean.toFixed(2)} (≥4.2)  ooc=${ooc} (=0)  mask=${masked} (=0)`);
  console.log(mean >= 4.2 && ooc === 0 && masked === 0 ? "PASS" : "FAIL");
  fs.writeFileSync(path.join(__dirname, "judge-result.json"), JSON.stringify(rows, null, 2));
})().catch((e) => { console.error(e); process.exit(1); });
