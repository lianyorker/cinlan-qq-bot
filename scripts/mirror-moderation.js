// Mirror moderation.go parity into cinlan-bot core (CRLF file — preserve EOL).
const fs = require('fs');
const core = 'D:/Company/cinlan/apps/cinlan-bot/core/bot/moderation.go';
const qq = 'D:/Company/cinlan/apps/cinlan-qq-bot/internal/bot/moderation.go';
let t = fs.readFileSync(core, 'utf8');
const q = fs.readFileSync(qq, 'utf8');
const eol = t.includes('\r\n') ? '\r\n' : '\n';
const crlf = (s) => s.replace(/\r?\n/g, eol);

const block = (src, startMark, endMark) => {
	const a = src.indexOf(startMark);
	const b = src.indexOf(endMark, a);
	if (a < 0 || b < 0) throw new Error('src block miss: ' + startMark);
	return src.slice(a, b);
};
const replaceBlock = (dst, startMark, endMark, withBlock) => {
	const a = dst.indexOf(startMark);
	const b = dst.indexOf(endMark, a);
	if (a < 0 || b < 0) throw new Error('dst block miss: ' + startMark);
	return dst.slice(0, a) + withBlock + dst.slice(b);
};

// 1) moderationSuspectPattern — qq-bot version (insult line + \b latin tokens)
t = replaceBlock(
	t,
	'var moderationSuspectPattern = regexp.MustCompile(',
	'const spamJudgeSystemPrompt',
	crlf(block(q, 'var moderationSuspectPattern = regexp.MustCompile(', 'const spamJudgeSystemPrompt'))
);
// 2) spamJudgeSystemPrompt — qq-bot version (辱骂 category + self-deprecation carve-out)
t = replaceBlock(
	t,
	'const spamJudgeSystemPrompt = `',
	'type spamVerdict struct',
	crlf(block(q, 'const spamJudgeSystemPrompt = `', 'type spamVerdict struct'))
);

// 3) Humanized notice templates + turn-stamp strip in voiced notices
const swaps = [
	['"检测到疑似广告内容已撤回（第 %d/%d 次警告，自动处理失败请管理员介入）"',
		'"广告撤了 但处理没成功 管理来看下（警告 %d/%d）"', 'fail-notice'],
	['"多次发布广告内容，已%s，警告记录已清零。"',
		'"广告发太多次了 %s 账清了"', 'action-notice'],
	['"检测到疑似广告内容已撤回（第 %d/%d 次警告，达到 %d 次将被处理）"',
		'"广告我撤了 别再发了（警告 %d/%d 到 %d 次要动手了）"', 'warn-notice'],
	['line = strings.Trim(line, "\\"\'「」“”")',
		'line = turnStampPattern.ReplaceAllString(line, "")' + eol +
		'\tline = strings.Trim(line, "\\"\'「」“” ")', 'stamp-strip'],
];
for (const [o, n, label] of swaps) {
	if (!t.includes(o)) throw new Error('anchor miss: ' + label);
	t = t.replace(o, n);
}
fs.writeFileSync(core, t);
console.log('moderation mirrored:',
	t.includes('bnmsl'), t.includes('辱骂'), t.includes('广告我撤了'),
	t.includes('turnStampPattern'));
