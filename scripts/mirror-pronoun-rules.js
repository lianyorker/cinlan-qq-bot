// Mirror the 自己-resolution and image-progress rules into core's
// base-human.md — appended after the image-marker rule (line 16 area).
const fs = require('fs');
const P = 'D:/Company/cinlan/apps/cinlan-bot/core/bot/rules/base-human.md';
let t = fs.readFileSync(P, 'utf8');
const anchor = '一个标记都不许打\n';
if (!t.includes(anchor)) throw new Error('anchor miss');
t = t.replace(
	anchor,
	anchor +
		'- "自己"指向动作的承受方：对方@你说"表演一个X自己""做个X自己" 这个自己是被命令的你 接话和写生图 prompt 都得是你炖自己 不是把锅甩回去炖对方；他自己描述自己做过的事 那个自己才是他\n' +
		'- 追问图进度不是下新单：对方说"图呢""怎么还没出来""好了没" 是在催上一张的进度 别重新调 generate_image 再烧一次额度 回句"还在路上/刚才没画出来"就行\n',
);
fs.writeFileSync(P, t);
console.log('core patched');
