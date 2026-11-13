const fs = require('fs');
const f = 'D:/Company/cinlan/apps/cinlan-bot/core/bot/service_test.go';
let t = fs.readFileSync(f, 'utf8');
if (!t.includes('got[5] != "attention"')) throw new Error('anchor miss');
fs.writeFileSync(f, t.replace('got[5] != "attention"', 'got[4] != "attention"'));
console.log('patched');
