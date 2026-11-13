// Mirror the recall-gated moderation notice into cinlan-bot core.
// core/moderation.go may be CRLF — normalize EOLs on read, restore on write.
const fs = require('fs');
const core = 'D:/Company/cinlan/apps/cinlan-bot/core/bot/moderation.go';
let t = fs.readFileSync(core, 'utf8');
const eol = t.includes('\r\n') ? '\r\n' : '\n';
if (eol === '\r\n') t = t.replace(/\r\n/g, '\n');

const swaps = [];
// 1) Log line: spam message recalled -> flagged + recalled field.
swaps.push([
	'"spam message recalled"',
	'"spam message flagged"',
	'log-tag',
]);
swaps.push([
	'"reason", reason,\n\t\t"warning", fmt.Sprintf("%d/%d", count, limit),',
	'"reason", reason,\n\t\t"recalled", recalled,\n\t\t"warning", fmt.Sprintf("%d/%d", count, limit),',
	'log-field',
]);
// 2) Action-failure notice: no false recall claim.
swaps.push([
	'"广告撤了 但处理没成功 管理来看下（警告 %d/%d）"',
	'"处理没成功 管理来看下（警告 %d/%d）"',
	'fail-notice',
]);
// 3) Warn notice only when we actually recalled.
swaps.push([
	'\t} else {\n\t\tnotice = fmt.Sprintf(\n\t\t\t"广告我撤了 别再发了（警告 %d/%d 到 %d 次要动手了）",\n\t\t\tcount, limit, limit,\n\t\t)\n\t}',
	'\t} else if recalled {\n\t\tnotice = fmt.Sprintf(\n\t\t\t"广告我撤了 别再发了（警告 %d/%d 到 %d 次要动手了）",\n\t\t\tcount, limit, limit,\n\t\t)\n\t}',
	'warn-gate',
]);
// 4) sendChain wrapped in notice != "".
swaps.push([
	'\tnotice = s.personaModerationNotice(ctx, state, notice)\n' +
	'\tif !s.sendChain(\n' +
	'\t\tctx,\n' +
	'\t\tstate.route(),\n' +
	'\t\tstate.userID,\n' +
	'\t\tstate.event.SenderName,\n' +
	'\t\t"",\n' +
	'\t\tmessage.Chain{message.At(state.userID), message.Text(" " + notice)},\n' +
	'\t) {\n' +
	'\t\ts.stats.sendErrors.Add(1)\n' +
	'\t}',
	'\tif notice != "" {\n' +
	'\t\tnotice = s.personaModerationNotice(ctx, state, notice)\n' +
	'\t\tif !s.sendChain(\n' +
	'\t\t\tctx,\n' +
	'\t\t\tstate.route(),\n' +
	'\t\t\tstate.userID,\n' +
	'\t\t\tstate.event.SenderName,\n' +
	'\t\t\t"",\n' +
	'\t\t\tmessage.Chain{message.At(state.userID), message.Text(" " + notice)},\n' +
	'\t\t) {\n' +
	'\t\t\ts.stats.sendErrors.Add(1)\n' +
	'\t\t}\n' +
	'\t}',
	'send-gate',
]);
// 5) Explanatory comment ahead of `var notice string`.
swaps.push([
	'\tvar notice string\n\tif count >= limit',
	'\t// Public replies only claim what actually happened: announce the\n' +
	'\t// warning when we removed the message ourselves, or a real action\n' +
	'\t// outcome — never pretend a recall that failed (another admin or\n' +
	'\t// bot may already have cleaned it up; the warning still counted\n' +
	'\t// silently so repeat offenders keep climbing the ladder).\n' +
	'\tvar notice string\n\tif count >= limit',
	'comment',
]);
for (const [oldS, newS, tag] of swaps) {
	if (!t.includes(oldS)) {
		throw new Error('anchor miss: ' + tag + ' -> ' + oldS.slice(0, 60));
	}
	t = t.replace(oldS, newS);
	console.log('patched', tag);
}
if (eol === '\r\n') t = t.replace(/\n/g, '\r\n');
fs.writeFileSync(core, t);
console.log('core moderation.go mirrored; recalled gate present:', t.includes('else if recalled'));
