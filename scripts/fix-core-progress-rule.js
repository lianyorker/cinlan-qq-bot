// Fix the image-progress rule wording in core's base-human.md:
// a failed generation is not "on the way" — tell the truth and retry later.
const fs = require('fs');
const P = 'D:/Company/cinlan/apps/cinlan-bot/core/bot/rules/base-human.md';
let t = fs.readFileSync(P, 'utf8');
const o = '别重新调 generate_image 再烧一次额度 回句"还在路上/刚才没画出来"就行';
if (!t.includes(o)) throw new Error('anchor miss');
t = t.replace(
	o,
	'别重新调 generate_image 再烧一次额度；上一张要是失败了就说"刚才没画出来 等下再试" 别骗对方说还在画',
);
fs.writeFileSync(P, t);
console.log('core fixed');
