// Mirror the llm_all moderation upgrade into cinlan-bot core.
const fs = require('fs');
const rep = (file, o, n, tag) => {
	let t = fs.readFileSync(file, 'utf8');
	if (!t.includes(o)) throw new Error(tag + ' anchor miss in ' + file);
	fs.writeFileSync(file, t.replace(o, n));
	console.log(tag + ' ok');
};

const REG = 'D:/Company/cinlan/apps/cinlan-bot/core/binding/registry.go';
const MOD = 'D:/Company/cinlan/apps/cinlan-bot/core/bot/moderation.go';

rep(
	REG,
	'\tWarnLimit    int      `json:"warn_limit,omitempty"`',
	'\tWarnLimit    int      `json:"warn_limit,omitempty"`\n' +
		'\t// LLMAll upgrades llm_check from suspect-gated to judge-everything:\n' +
		'\t// every group message that escapes the strict patterns still gets a\n' +
		'\t// verdict — ads phrased to dodge the word list stop slipping through.\n' +
		'\tLLMAll bool `json:"llm_all,omitempty"`',
	'reg-llmall',
);

rep(
	MOD,
	'判定违规的常见特征：代充/代练/卖号、低价出售、留联系方式引流、不明链接、兼职刷单、色情资源、转发广告卡片、纯图片广告、连续营销话术',
	'判定违规的常见特征：代充/代练/卖号卖卡、低价出售、留联系方式引流、不明链接、兼职刷单、色情资源、转发广告卡片、纯图片广告、连续营销话术',
	'mod-prompt-1',
);
rep(
	MOD,
	'人身攻击或辱骂（如"傻逼/脑残/废物/你妈"类脏词指向具体人）。\n正常聊天',
	'人身攻击或辱骂（如"傻逼/脑残/废物/你妈"类脏词指向具体人）。\n' +
		'消息里的标记说明：[图片]表示带图，[转发]是转发的聊天记录，[JSON]/[XML]是分享卡片，[文件]/[视频]是附件——纯发图、转发卡片、挂附件的广告同样判违规。\n正常聊天',
	'mod-prompt-2',
);
rep(
	MOD,
	'\tdetected, reason := pol.Matches(state.text), ""\n\tif !detected && pol.LLMCheck &&\n\t\tmoderationSuspicious(state.event.Chain, state.text) {',
	'\tdetected, reason := pol.Matches(state.text), ""\n' +
		'\tjudge := pol.LLMCheck &&\n\t\tmoderationSuspicious(state.event.Chain, state.text)\n' +
		'\tif pol.LLMAll && !trivialMessage(state.event.Chain, state.text) {\n\t\tjudge = true\n\t}\n' +
		'\tif !detected && judge {',
	'mod-judge',
);
rep(
	MOD,
	'// moderationSuspicious reports whether a message carries spam vectors —',
	'// trivialMessage keeps llm_all from burning a judge call on one-word\n' +
		'// acknowledgements: plain text of a few runes with no rich components.\n' +
		'func trivialMessage(chain message.Chain, text string) bool {\n' +
		'\tfor _, component := range chain {\n' +
		'\t\tswitch component.Type {\n' +
		'\t\tcase message.TypeText, message.TypeAt, message.TypeReply:\n' +
		'\t\tdefault:\n\t\t\treturn false\n\t\t}\n\t}\n' +
		'\treturn len([]rune(strings.TrimSpace(text))) <= 3\n}\n\n' +
		'// moderationSuspicious reports whether a message carries spam vectors —',
	'mod-trivial',
);
