// One-off: mirror the turnStamp scrub into cinlan-bot core (LF preserved).
const fs = require('fs');

const rp = 'D:/Company/cinlan/apps/cinlan-bot/core/bot/reply_plan.go';
const pp = 'D:/Company/cinlan/apps/cinlan-bot/core/bot/pipeline.go';

// 1) reply_plan.go — pattern declaration before the markdown comment block.
let t = fs.readFileSync(rp, 'utf8');
const rpAnchor = '// These patterns catch the markdown syntax models keep emitting even';
if (!t.includes(rpAnchor)) throw new Error('reply_plan anchor miss');
const decl = `// turnStampPattern matches the [MM-DD HH:mm] / [HH:mm] turn stamps that
// decorate stored history. The model copies the format back when it
// quotes earlier lines — the stamp is prompt scaffolding, never chat
// content, so it dies here. 【】 variants come from CJK IME echo.
var turnStampPattern = regexp.MustCompile(
	\`[\\[【]\\d{1,2}-\\d{1,2}\\s+\\d{1,2}:\\d{2}[\\]】]\\s*|\` +
		\`[\\[【]\\d{1,2}:\\d{2}[\\]】]\\s*\`,
)

`;
t = t.replace(rpAnchor, decl + rpAnchor);

// 2) scrubMarkdown body — strip stamps first.
const scrubOld = 'func scrubMarkdown(text string) string {\n\ttext = selfTagPattern.ReplaceAllString(text, "")';
if (!t.includes(scrubOld)) throw new Error('scrubMarkdown anchor miss');
t = t.replace(
	scrubOld,
	'func scrubMarkdown(text string) string {\n\ttext = turnStampPattern.ReplaceAllString(text, "")\n\ttext = selfTagPattern.ReplaceAllString(text, "")'
);
fs.writeFileSync(rp, t);

// 3) pipeline.go — single-reply path scrub.
let p = fs.readFileSync(pp, 'utf8');
const ppOld = '\tif cleaned, refused := dropProviderRefusals(state.reply); refused {';
if (!p.includes(ppOld)) throw new Error('pipeline anchor miss');
const ppNew =
	'\t// Single-reply path sends state.reply raw — run the same scrub the\n' +
	'\t// per-part loop applies so turn stamps/markdown never leak either.\n' +
	'\tstate.reply = scrubMarkdown(state.reply)\n' +
	'\tif strings.TrimSpace(state.reply) == "" {\n' +
	'\t\treturn nil\n' +
	'\t}\n' +
	ppOld;
p = p.replace(ppOld, ppNew);
fs.writeFileSync(pp, p);

// verify
const v = fs.readFileSync(rp, 'utf8');
const w = fs.readFileSync(pp, 'utf8');
console.log('rp decl:', v.includes('turnStampPattern = regexp'), 'call:', v.includes('turnStampPattern.ReplaceAllString'));
console.log('pp scrub:', w.includes('state.reply = scrubMarkdown(state.reply)'));
