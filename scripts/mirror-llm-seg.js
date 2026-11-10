// Mirror the LLM reply-segmentation feature into cinlan-bot core:
// 1. core/bot/reply_split_llm.go      — new file, import paths swapped
// 2. core/bot/reply_split_llm_test.go — new file, import paths swapped
// 3. core/bot/reply_plan.go           — foldIntroBubble short-pair inline fold
// 4. core/config/config.go            — LLMReplySegment field + env parse
// 5. core/bot/pipeline.go             — judge-first planning branch
const fs = require('fs');
const path = require('path');

const SRC_BOT = path.resolve(__dirname, '../internal/bot');
const DST_BOT = 'D:/Company/cinlan/apps/cinlan-bot/core/bot';
const DST_CFG = 'D:/Company/cinlan/apps/cinlan-bot/core/config/config.go';
const SRC_MOD = 'github.com/lianyorker/cinlan-qq-bot/internal/';
const DST_MOD = 'github.com/lianyorker/cinlan-core/';

const log = (m) => console.log(m);
const eolOf = (t) => (t.includes('\r\n') ? '\r\n' : '\n');

// --- 1/2: new files -------------------------------------------------------
for (const name of ['reply_split_llm.go', 'reply_split_llm_test.go']) {
	let t = fs.readFileSync(path.join(SRC_BOT, name), 'utf8');
	t = t.split(SRC_MOD).join(DST_MOD);
	fs.writeFileSync(path.join(DST_BOT, name), t);
	log('wrote core/bot/' + name);
}

// --- 3: reply_plan.go foldIntroBubble -------------------------------------
{
	const p = path.join(DST_BOT, 'reply_plan.go');
	let t = fs.readFileSync(p, 'utf8');
	const eol = eolOf(t);
	const oldBlock = '\tparts[1] = intro + "\\n\\n" + parts[1]' + eol + '\treturn parts[1:]';
	const newBlock = [
		'\t// Casual pairs merge inline — a "\\n\\n" inside one bubble reads',
		'\t// staged to anyone who grew up on QQ. Walls keep the blank line:',
		'\t// a caption pasted over a long body still wants its own line.',
		'\tif len([]rune(parts[1])) <= 60 {',
		'\t\tparts[1] = foldJoin([]string{intro, parts[1]})',
		'\t} else {',
		'\t\tparts[1] = intro + "\\n\\n" + parts[1]',
		'\t}',
		'\treturn parts[1:]',
	].join(eol);
	if (!t.includes(oldBlock)) throw new Error('reply_plan foldIntro anchor missing');
	t = t.replace(oldBlock, newBlock);
	fs.writeFileSync(p, t);
	log('patched core/bot/reply_plan.go');
}

// --- 4: config.go ----------------------------------------------------------
{
	let t = fs.readFileSync(DST_CFG, 'utf8');
	const eol = eolOf(t);
	const fieldAnchor = '\tMemoryExtract  bool';
	const fieldAdd =
		fieldAnchor + eol +
		'\t// LLMReplySegment lets a judge-model pass re-segment each reply at' + eol +
		'\t// human "send key" positions instead of trusting the model\'s own' + eol +
		'\t// [[NEXT]] placements (fidelity-checked, falls back on failure).' + eol +
		'\t// Off = [[NEXT]] markers and mechanical folding decide all splits.' + eol +
		'\tLLMReplySegment bool';
	if (!t.includes(fieldAnchor)) throw new Error('config field anchor missing');
	if (!t.includes('LLMReplySegment')) t = t.replace(fieldAnchor, fieldAdd);

	const envAnchor = '\tcfg.MemoryExtract = parseBool("QQ_MEMORY_EXTRACT", true, &errs)';
	const envAdd = envAnchor + eol + '\tcfg.LLMReplySegment = parseBool("QQ_LLM_SEGMENT", true, &errs)';
	if (!t.includes(envAnchor)) throw new Error('config env anchor missing');
	if (!t.includes('QQ_LLM_SEGMENT')) t = t.replace(envAnchor, envAdd);
	fs.writeFileSync(DST_CFG, t);
	log('patched core/config/config.go');
}

// --- 5: pipeline.go --------------------------------------------------------
{
	const p = path.join(DST_BOT, 'pipeline.go');
	let t = fs.readFileSync(p, 'utf8');
	const eol = eolOf(t);
	const callLine = '\t\t\tplanned := planReplyParts(state.reply, maxParts)' + eol;
	const i = t.indexOf(callLine);
	if (i < 0) throw new Error('pipeline planReplyParts anchor missing');
	// The if-block right after the call, ending just before `var truncated`.
	const tailMark = '\n\t\t\tvar truncated bool';
	const j = t.indexOf(tailMark, i);
	if (j < 0) throw new Error('pipeline tail anchor missing');
	const ifBlock = t.slice(i + callLine.length, j); // starts with the softSplit if
	const reindented = ifBlock.split(eol).map((l) => (l === '' ? l : '\t' + l)).join(eol);
	const replacement =
		'\t\t\tvar planned []string' + eol +
		'\t\t\tif segs, ok := s.llmSegmentReply(ctx, state.reply, maxParts); ok {' + eol +
		'\t\t\t\t// Judge-model segmentation is authoritative — even a' + eol +
		'\t\t\t\t// single-segment verdict beats the mood-rolled split.' + eol +
		'\t\t\t\tplanned = segs' + eol +
		'\t\t\t} else {' + eol +
		'\t\t\t\tplanned = planReplyParts(state.reply, maxParts)' + eol +
		reindented +
		'\t\t\t}' + eol;
	t = t.slice(0, i) + replacement + t.slice(j);
	fs.writeFileSync(p, t);
	log('patched core/bot/pipeline.go');
}
console.log('mirror done');
