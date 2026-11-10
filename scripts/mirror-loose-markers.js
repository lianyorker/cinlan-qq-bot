// Mirror upgradeLooseMarkers into cinlan-bot core:
// 1. core/bot/reply_plan.go  — looseMarkerPattern + upgradeLooseMarkers
// 2. core/bot/pipeline.go    — upgrade call before voice/PASS extraction
// 3. core/bot/reply_plan_test.go — the five new test funcs
const fs = require('fs');
const path = require('path');

const SRC_BOT = path.resolve(__dirname, '../internal/bot');
const DST_BOT = 'D:/Company/cinlan/apps/cinlan-bot/core/bot';
const eolOf = (t) => (t.includes('\r\n') ? '\r\n' : '\n');
const toEol = (s, eol) => s.replace(/\r?\n/g, eol);

// --- 1: reply_plan.go helpers ----------------------------------------------
{
	const src = fs.readFileSync(path.join(SRC_BOT, 'reply_plan.go'), 'utf8');
	const p = path.join(DST_BOT, 'reply_plan.go');
	let t = fs.readFileSync(p, 'utf8');
	const eol = eolOf(t);
	const START = '// looseMarkerPattern catches single-bracket spellings';
	const END = 'func needsFoldComma(prev, next string) bool {';
	const a = src.indexOf(START), b = src.indexOf(END);
	if (a < 0 || b < 0) throw new Error('src block missing');
	const block = src.slice(a, b);
	const dA = t.indexOf('func needsFoldComma(prev, next string) bool {');
	if (dA < 0) throw new Error('dst needsFoldComma missing');
	if (!t.includes('looseMarkerPattern')) {
		t = t.slice(0, dA) + toEol(block, eol) + t.slice(dA);
		fs.writeFileSync(p, t);
	}
	console.log('patched core/bot/reply_plan.go');
}

// --- 2: pipeline.go call site -----------------------------------------------
{
	const p = path.join(DST_BOT, 'pipeline.go');
	let t = fs.readFileSync(p, 'utf8');
	const eol = eolOf(t);
	const anchor = '\tvar voiceClips []voiceClip';
	if (t.indexOf(anchor) < 0) throw new Error('voiceClips anchor missing');
	if (!t.includes('upgradeLooseMarkers(state.reply)')) {
		const add =
			'\t// The model writes [POKE] for [[POKE]] single-bracket often enough' + eol +
			'\t// that upgrading beats leaking literal marker text — run this before' + eol +
			'\t// voice/PASS/segmentation so every downstream parser sees real [[..]].' + eol +
			'\tstate.reply = upgradeLooseMarkers(state.reply)' + eol;
		t = t.replace(anchor, add + anchor);
		fs.writeFileSync(p, t);
	}
	console.log('patched core/bot/pipeline.go');
}

// --- 3: test file -------------------------------------------------------------
{
	const src = fs.readFileSync(path.join(SRC_BOT, 'reply_plan_test.go'), 'utf8');
	const p = path.join(DST_BOT, 'reply_plan_test.go');
	let t = fs.readFileSync(p, 'utf8');
	const eol = eolOf(t);
	const START = 'func TestUpgradeLooseMarkersUpgradesSingles';
	const END = 'func TestPlanReplyPartsIntroNeverMergesIntoMarker';
	const a = src.indexOf(START), b = src.indexOf(END);
	if (a < 0 || b < 0) throw new Error('test block missing in src');
	const block = src.slice(a, b);
	if (!t.includes('TestUpgradeLooseMarkersUpgradesSingles')) {
		const dA = t.indexOf(END);
		if (dA < 0) throw new Error('test anchor missing in dst');
		t = t.slice(0, dA) + toEol(block, eol) + t.slice(dA);
		fs.writeFileSync(p, t);
	}
	console.log('patched core/bot/reply_plan_test.go');
}
console.log('mirror done');
