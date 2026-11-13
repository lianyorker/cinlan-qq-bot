// Mirror the moderation wiring fix into cinlan-bot core:
// 1) rawRule.Moderation field + LoadFile mapping (core/binding/config.go)
// 2) state.moderation = current.Moderation (core/bot/pipeline.go)
// 3) suspect pattern += account-selling jargon (core/bot/moderation.go)
const fs = require('fs');
const rep = (file, o, n, tag) => {
	let t = fs.readFileSync(file, 'utf8');
	if (!t.includes(o)) throw new Error(tag + ' anchor miss in ' + file);
	fs.writeFileSync(file, t.replace(o, n));
	console.log(tag + ' ok');
};

const CFG = 'D:/Company/cinlan/apps/cinlan-bot/core/binding/config.go';
const PIPE = 'D:/Company/cinlan/apps/cinlan-bot/core/bot/pipeline.go';
const MOD = 'D:/Company/cinlan/apps/cinlan-bot/core/bot/moderation.go';

// 1a. rawRule field — core uses the same flat struct layout.
rep(
	CFG,
	'\tReplyPolicy     *ReplyPolicy     `json:"reply_policy"`\n\tLearningEnabled',
	'\tReplyPolicy     *ReplyPolicy     `json:"reply_policy"`\n' +
		'\t// Moderation carries the per-binding spam ladder; without it\n' +
		'\t// configured groups get no recall/warn enforcement at all.\n' +
		'\tModeration      *ModerationPolicy `json:"moderation"`\n\tLearningEnabled',
	'cfg-field',
);

// 1b. LoadFile mapping.
rep(
	CFG,
	'\t\t\tReplyPolicy:     raw.ReplyPolicy,\n\t\t\tLearningEnabled:',
	'\t\t\tReplyPolicy:     raw.ReplyPolicy,\n\t\t\tModeration:      raw.Moderation,\n\t\t\tLearningEnabled:',
	'cfg-map',
);

// 2. flowState assignment during binding resolution.
rep(
	PIPE,
	'\t\tstate.learning = s.cfg.SessionLearning &&',
	'\t\tstate.moderation = current.Moderation\n\t\tstate.learning = s.cfg.SessionLearning &&',
	'pipe-assign',
);

// 3. suspect pattern — same concatenated literal as qq-bot.
rep(
	MOD,
	'`推广|引流|接单|赚钱|兼职|优惠|特价|免费领|出售|转让|回收|卖号|刷单|日结|` +',
	'`推广|引流|接单|赚钱|兼职|优惠|特价|免费领|出售|转让|回收|卖号|刷单|日结|` +\n' +
		'\t\t`实卡|成品号|租号|号商|卡商|卡密|` +',
	'mod-pattern',
);
