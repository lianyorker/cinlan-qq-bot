// Mirror into cinlan-bot core:
// 1) attention.go — owner group messages no longer skip the classifier
// 2) media/image.go — generate_image description gains the explicit-request gate
const fs = require('fs');
const rep = (file, oldStr, newStr) => {
	let t = fs.readFileSync(file, 'utf8');
	if (!t.includes(oldStr)) throw new Error('anchor miss ' + file + ': ' + oldStr.slice(0, 40));
	t = t.replace(oldStr, newStr);
	fs.writeFileSync(file, t);
	console.log('patched ' + file);
};
const CORE = 'D:/Company/cinlan/apps/cinlan-bot/core';

rep(CORE + '/bot/attention.go',
	'direct := state.eventMentioned ||\n' +
	'		state.replyPolicy.AllowsUser(state.userID) ||\n' +
	'		s.isOwnerUser(state.userID)',
	'// Privilege is not attention: the owner\'s group broadcasts still go\n' +
	'	// through the classifier — he keeps privileged prompt treatment and\n' +
	'	// mention/command paths, he just doesn\'t force a reply to every line.\n' +
	'	direct := state.eventMentioned ||\n' +
	'		state.replyPolicy.AllowsUser(state.userID) ||\n' +
	'		(s.isOwnerUser(state.userID) &&\n' +
	'			state.chatType == platform.ChatPrivate)');

rep(CORE + '/media/image.go',
	'"用户要求画图、生成图片或发一张图片时必须调用；不要只返回提示词。"',
	'"仅当用户当前明确要图（画图/生成图片/发图）时才调用——不得自行配图、玩梗生图、" +\n' +
	'			"或给公告/消息配插图；不要只返回提示词。"');
