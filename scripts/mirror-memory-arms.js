// Mirror the Hindsight-inspired memory arms into cinlan-bot core:
// ① observation dedup + Proofs  ② temporal chunk filter
// ③ MemberMemory entity bundle  ④ async retain extract  ⑤ on-demand reflect.
const fs = require('fs');
const CORE = 'D:/Company/cinlan/apps/cinlan-bot/core';
const QQ = 'D:/Company/cinlan/apps/cinlan-qq-bot/internal';

const must = (t, s, label) => {
	if (!t.includes(s)) throw new Error('anchor miss: ' + label);
	return t;
};
const swap = (t, oldS, newS, label) => {
	must(t, oldS, label);
	return t.replace(oldS, newS);
};
const write = (p, c) => fs.writeFileSync(p, c);
const read = (p) => fs.readFileSync(p, 'utf8');

// ---- 1) core/chatprefs/store.go ----
let st = read(CORE + '/chatprefs/store.go');
const qqSt = read(QQ + '/chatprefs/store.go');
// Proofs field — copy the exact block from qq-bot (same file shape).
const proofsAnchor = '	Prev string `json:"prev,omitempty"`\n}';
st = swap(st, proofsAnchor,
	'	Prev string `json:"prev,omitempty"`\n' +
	'	// Proofs counts how many times independent evidence re-stated this\n' +
	'	// belief — Hindsight-style observation strength. 0/1 = seen once;\n' +
	'	// each re-confirmation refines the line instead of appending a twin.\n' +
	'	Proofs int `json:"proofs,omitempty"`\n}',
	'store Proofs');
// helpers + observationMatch — extract verbatim from qq-bot.
const helpersStart = qqSt.indexOf('// subsetBigrams reports');
const helpersEnd = qqSt.indexOf('// HybridTextScore applies');
const helpers = qqSt.slice(helpersStart, helpersEnd);
st = swap(st, '// HybridTextScore applies', helpers + '// HybridTextScore applies',
	'store helpers');
// dedup block in addFactLocked — extract verbatim.
const dedupStart = qqSt.indexOf('	// Observation merge: the same belief restated');
const dedupEnd = qqSt.indexOf('	// Conflict marker: same-topic');
const dedup = qqSt.slice(dedupStart, dedupEnd);
st = swap(st, '	// Conflict marker: same-topic', dedup + '	// Conflict marker: same-topic',
	'store dedup');
// MemberMemory — extract verbatim, insert after ImpressionTexts func.
const mmStart = qqSt.indexOf('// MemberMemory is the entity-graph arm');
const mmEnd = qqSt.indexOf('// HasFactPrefix');
const mm = qqSt.slice(mmStart, mmEnd);
st = swap(st, '// HasFactPrefix', mm + '// HasFactPrefix', 'store MemberMemory');
write(CORE + '/chatprefs/store.go', st);
console.log('store.go: Proofs', st.includes('Proofs int'),
	'obsMatch', st.includes('func observationMatch'),
	'MemberMemory', st.includes('func (s *Store) MemberMemory'));

// ---- 2) core/bot/pipeline.go ----
let pl = read(CORE + '/bot/pipeline.go');
const qqPl = read(QQ + '/bot/pipeline.go');
// surfacedFacts + skip set
pl = swap(pl,
	'		for _, f := range s.chatPrefs.RecallFacts(state.platform, state.selfID, state.chatType, state.chatID, recallQuery, recallLimit) {',
	'		surfacedFacts := map[string]bool{}\n' +
	'		for _, f := range s.chatPrefs.RecallFacts(state.platform, state.selfID, state.chatType, state.chatID, recallQuery, recallLimit) {',
	'pipeline surfacedFacts decl');
pl = swap(pl,
	'			recalledRunes += len([]rune(f.Text))',
	'			surfacedFacts[f.Text] = true\n' +
	'			recalledRunes += len([]rune(f.Text))',
	'pipeline surfacedFacts mark');
// member bundle + reflect injection — verbatim from qq-bot.
const injectStart = qqPl.indexOf('		// Entity arm: the query names a member');
const injectEnd = qqPl.indexOf('		// Long-term verbatim index:');
const inject = qqPl.slice(injectStart, injectEnd);
pl = swap(pl, '		// Long-term verbatim index:', inject + '		// Long-term verbatim index:',
	'pipeline member inject');
// temporal window in recallHistoryChunks — verbatim block.
const twStart = qqPl.indexOf('	// Temporal arm: a named period');
const twEnd = qqPl.indexOf('	type hit struct {');
const tw = qqPl.slice(twStart, twEnd);
pl = swap(pl, '	type hit struct {', tw + '	type hit struct {', 'pipeline temporal');
// recordExchange hook
pl = swap(pl,
	'	s.scheduleCompression(ctx, state)',
	'	s.maybeExtractMemory(state)\n' + '	s.scheduleCompression(ctx, state)',
	'pipeline recordExchange');
write(CORE + '/bot/pipeline.go', pl);
console.log('pipeline.go: surfaced', pl.includes('surfacedFacts := map'),
	'MemberMemory', pl.includes('s.chatPrefs.MemberMemory'),
	'temporal', pl.includes('recallTimeWindow'),
	'extract', pl.includes('s.maybeExtractMemory(state)'));

// ---- 3) core/bot/service.go — fields ----
let sv = read(CORE + '/bot/service.go');
sv = swap(sv,
	'	judgeProvider string',
	'	judgeProvider string\n' +
	'	// extractLast throttles the background retain pass per session —\n' +
	'	// one extraction burst per chat per window, not one per message.\n' +
	'	extractLast   sync.Map // sessionID -> time.Time\n' +
	'	// reflectLast throttles the on-demand synthesis pass — "他最近咋样"\n' +
	'	// re-synthesizes at most once per session per window.\n' +
	'	reflectLast   sync.Map // sessionID -> time.Time',
	'service fields');
write(CORE + '/bot/service.go', sv);
console.log('service.go: fields', sv.includes('extractLast   sync.Map'));

// ---- 4) core/config/config.go ----
let cf = read(CORE + '/config/config.go');
cf = swap(cf,
	'	FollowUpAnyUser bool',
	'	FollowUpAnyUser bool\n' +
	'	// MemoryExtract is the retain arm: after each exchange a background\n' +
	'	// judge-model pass pulls long-term-worthy facts out of the turn and\n' +
	'	// feeds the fact store (dedup/refine gates apply). Off = only the\n' +
	'	// persona\'s own in-chat remember tool writes memories.\n' +
	'	MemoryExtract bool',
	'config field');
cf = swap(cf,
	'cfg.FollowUpAnyUser = parseBool("QQ_FOLLOW_UP_ANY_USER", false, &errs)',
	'cfg.FollowUpAnyUser = parseBool("QQ_FOLLOW_UP_ANY_USER", false, &errs)\n' +
	'	cfg.MemoryExtract = parseBool("QQ_MEMORY_EXTRACT", true, &errs)',
	'config parse');
write(CORE + '/config/config.go', cf);
console.log('config.go: MemoryExtract', cf.includes('MemoryExtract bool'),
	'env', cf.includes('QQ_MEMORY_EXTRACT'));

// ---- 5) new files ----
let ex = read(QQ + '/bot/extract.go');
ex = ex.replaceAll('github.com/lianyorker/cinlan-qq-bot/internal/',
	'github.com/lianyorker/cinlan-core/');
write(CORE + '/bot/extract.go', ex);
write(CORE + '/bot/time_window.go', read(QQ + '/bot/time_window.go'));
console.log('new files written: extract.go, time_window.go');
