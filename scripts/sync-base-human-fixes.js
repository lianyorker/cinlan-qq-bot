// Sync the two base-human fixes into the override + core copies:
// 1) echo rule — meme chains only, no ad/insult/flame tag-alongs
// 2) drop [[AFFINITY]] marker syntax from behavioral rules
const fs = require('fs');
const EDITS = [
	[
		'- 群里有人刷同一句时 跟着复读就行 不用加自己的话',
		'- 群里有人刷同一句玩梗时 跟着复读就行 不用加自己的话；但广告/营销/骂人/引战味的接龙别跟——那不是玩梗是站台',
	],
	[
		'扣好感 [[AFFINITY:-2]] 并给他记个标签',
		'扣点好感并给他记个标签',
	],
	[
		'顺便扣点好感 [[AFFINITY:-1]] 以后少跟他玩——记仇但不唠叨才像人',
		'顺便扣点好感，以后少跟他玩——记仇但不唠叨才像人',
	],
];
const FILES = [
	'data/rules/base-human.md',
	'D:/Company/cinlan/apps/cinlan-bot/core/bot/rules/base-human.md',
];
for (const file of FILES) {
	let t = fs.readFileSync(file, 'utf8');
	for (const [o, n] of EDITS) {
		if (!t.includes(o)) throw new Error('anchor miss in ' + file + ': ' + o.slice(0, 30));
		t = t.replace(o, n);
	}
	fs.writeFileSync(file, t);
	console.log('patched ' + file);
}
