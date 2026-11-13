// Mirror to cinlan-bot core:
// 1) run stageModeration inside stageWake (before ignore gates), drop
//    the standalone moderation pipeline stage
// 2) echo-chain spam guard in tryEchoJoin
// 3) judge verdict logging in classifySpam
const fs = require('fs');
const rep = (file, o, n, tag) => {
	let t = fs.readFileSync(file, 'utf8');
	if (!t.includes(o)) throw new Error(tag + ' anchor miss in ' + file);
	fs.writeFileSync(file, t.replace(o, n));
	console.log(tag + ' ok');
};

const PIPE = 'D:/Company/cinlan/apps/cinlan-bot/core/bot/pipeline.go';
const SPAM = 'D:/Company/cinlan/apps/cinlan-bot/core/bot/spam_repeat.go';
const MOD = 'D:/Company/cinlan/apps/cinlan-bot/core/bot/moderation.go';

// 1a. wake-internal moderation call, before voice sampling.
rep(
	PIPE,
	'\t// Voice sampling: fold this message into the sender\'s learned\n' +
		'\t// style profile. Read-only on behavior — it never gates anything.\n' +
		'\tif s.chatPrefs != nil && !state.pokeEvent {\n' +
		'\t\ts.chatPrefs.ObserveUserStyle(',
	'\t// Moderation gets first dibs ahead of every ignore gate below —\n' +
		'\t// mention-only, self-mute, fatigue, quiet hours. An ad shouldn\'t\n' +
		'\t// survive just because nobody @-ed her or the group was silenced;\n' +
		'\t// recall is a group-safety action, not a reply.\n' +
		'\tif err := s.stageModeration(ctx, event); err != nil {\n' +
		'\t\treturn err\n\t}\n\tif state.ignored {\n\t\treturn nil\n\t}\n' +
		'\t// Voice sampling: fold this message into the sender\'s learned\n' +
		'\t// style profile. Read-only on behavior — it never gates anything.\n' +
		'\tif s.chatPrefs != nil && !state.pokeEvent {\n' +
		'\t\ts.chatPrefs.ObserveUserStyle(',
	'pipe-wake-mod',
);

// 1b. remove the standalone stage.
rep(
	PIPE,
	'\t\tpipeline.StageFunc{StageName: "command", Handler: s.stageCommand},\n' +
		'\t\tpipeline.StageFunc{StageName: "moderation", Handler: s.stageModeration},',
	'\t\tpipeline.StageFunc{StageName: "command", Handler: s.stageCommand},',
	'pipe-stage-drop',
);

// 2. echo-chain spam guard.
rep(
	SPAM,
	'key := state.platform + ":" + state.chatType + ":" + state.chatID\n' +
		'\tif s.noteEchoChain(key, state.userID, state.text) < echoChainMinUsers {\n' +
		'\t\treturn ""\n\t}',
	'key := state.platform + ":" + state.chatType + ":" + state.chatID\n' +
		'\tif s.noteEchoChain(key, state.userID, state.text) < echoChainMinUsers {\n' +
		'\t\treturn ""\n\t}\n' +
		'\t// Never tag along on a spam chain — repeating an ad verbatim gets\n' +
		'\t// her flagged right alongside the accounts spamming it.\n' +
		'\tif moderationSuspicious(state.event.Chain, state.text) {\n' +
		'\t\treturn ""\n\t}',
	'echo-guard',
);

// 3. judge verdict log.
rep(
	MOD,
	'verdict, err := decodeSpamVerdict(response.Reply)\n' +
		'\tif err != nil {\n\t\treturn false, "", err\n\t}\n' +
		'\tif verdict.Confidence == nil ||\n\t\t*verdict.Confidence < threshold {',
	'verdict, err := decodeSpamVerdict(response.Reply)\n' +
		'\tif err != nil {\n\t\treturn false, "", err\n\t}\n' +
		'\tconfidence := float64(-1)\n\tif verdict.Confidence != nil {\n' +
		'\t\tconfidence = *verdict.Confidence\n\t}\n' +
		'\ts.logger.Info(\n\t\t"spam judge verdict",\n\t\t"chat", state.chatID,\n' +
		'\t\t"user", state.userID,\n\t\t"spam", verdict.Spam,\n' +
		'\t\t"confidence", confidence,\n\t\t"threshold", threshold,\n' +
		'\t\t"reason", strings.TrimSpace(verdict.Reason),\n\t)\n' +
		'\tif verdict.Confidence == nil ||\n\t\t*verdict.Confidence < threshold {',
	'judge-log',
);
