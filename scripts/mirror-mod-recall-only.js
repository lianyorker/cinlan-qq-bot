// Mirror the recall_only moderation action into cinlan-bot core.
const fs = require('fs');
const root = 'D:/Company/cinlan/apps/cinlan-bot/core';

function patch(path, swaps) {
	let t = fs.readFileSync(path, 'utf8');
	const eol = t.includes('\r\n') ? '\r\n' : '\n';
	if (eol === '\r\n') t = t.replace(/\r\n/g, '\n');
	for (const [oldS, newS, tag] of swaps) {
		if (!t.includes(oldS)) {
			throw new Error(path + ' anchor miss: ' + tag);
		}
		t = t.replace(oldS, newS);
		console.log('patched', path, tag);
	}
	if (eol === '\r\n') t = t.replace(/\n/g, '\r\n');
	fs.writeFileSync(path, t);
}

patch(root + '/binding/registry.go', [
	[
		'\tModerationActionWarnOnly = "warn_only" // recall + warn, never mute/kick\n' +
		')',
		'\tModerationActionWarnOnly = "warn_only" // recall + warn, never mute/kick\n' +
		'\t// recall_only deletes the spam and stops there — no warn ladder, no\n' +
		'\t// public notice, no mute/kick. For chats where another admin or bot\n' +
		'\t// already handles enforcement and we only want the message gone.\n' +
		'\tModerationActionRecallOnly = "recall_only"\n' +
		')',
		'const',
	],
	[
		'\tcase ModerationActionWarnOnly:\n' +
		'\t\treturn ModerationActionWarnOnly\n' +
		'\tdefault:',
		'\tcase ModerationActionWarnOnly:\n' +
		'\t\treturn ModerationActionWarnOnly\n' +
		'\tcase ModerationActionRecallOnly:\n' +
		'\t\treturn ModerationActionRecallOnly\n' +
		'\tdefault:',
		'effective',
	],
]);

patch(root + '/bot/moderation.go', [
	[
		'\tkey := state.platform + ":" + state.chatType + ":" +\n' +
		'\t\tstate.chatID + ":" + state.userID\n' +
		'\tcount := s.warnStore.Warn(key)\n' +
		'\tlimit := pol.EffectiveWarnLimit()\n' +
		'\taction := pol.EffectiveAction()',
		'\taction := pol.EffectiveAction()\n' +
		'\tif action == binding.ModerationActionRecallOnly {\n' +
		'\t\t// Delete-and-done: spam is gone (or at least attempted), nothing\n' +
		'\t\t// else to enforce — no warn counter, no public notice.\n' +
		'\t\ts.logger.Info(\n' +
		'\t\t\t"spam message flagged",\n' +
		'\t\t\t"chat", state.chatID,\n' +
		'\t\t\t"user", state.userID,\n' +
		'\t\t\t"via", map[bool]string{true: "llm", false: "pattern"}[reason != ""],\n' +
		'\t\t\t"reason", reason,\n' +
		'\t\t\t"recalled", recalled,\n' +
		'\t\t\t"action", action,\n' +
		'\t\t)\n' +
		'\t\tstate.ignored = true\n' +
		'\t\tevent.Stop("moderation")\n' +
		'\t\treturn nil\n' +
		'\t}\n' +
		'\n' +
		'\tkey := state.platform + ":" + state.chatType + ":" +\n' +
		'\t\tstate.chatID + ":" + state.userID\n' +
		'\tcount := s.warnStore.Warn(key)\n' +
		'\tlimit := pol.EffectiveWarnLimit()',
		'gate',
	],
]);
console.log('done');
