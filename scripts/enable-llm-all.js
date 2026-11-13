// Enable llm_all on every binding that has a moderation block.
// Run on the server: node /tmp/enable-llm-all.js
const fs = require('fs');
const P = '/opt/codepany/cinlan-qq-bot/bundle/data/chat-bindings.json';
const data = JSON.parse(fs.readFileSync(P, 'utf8'));
let touched = 0;
for (const rule of data.bindings) {
	if (rule.moderation && typeof rule.moderation === 'object') {
		rule.moderation.llm_all = true;
		touched++;
		console.log(rule.name, '-> llm_all=true');
	}
}
if (touched === 0) throw new Error('no moderation blocks found');
fs.writeFileSync(P, JSON.stringify(data, null, 2) + '\n');
console.log('patched', touched, 'bindings');
