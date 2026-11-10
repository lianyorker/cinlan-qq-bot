// Mirror the EmojiLikeTool fix (name/id resolution instead of raw
// codepoint) from qq-bot internal/interaction/tools.go into
// cinlan-bot core/interaction/tools.go.
// Copies the whole block from "// emojiLikeIDs mirrors" through
// EmojiLikeTool's closing brace, plus the strconv import.
// Preserves the core file's existing line endings.
const fs = require('fs');
const path = require('path');

const SRC = path.resolve(__dirname, '../internal/interaction/tools.go');
const DST = 'D:/Company/cinlan/apps/cinlan-bot/core/interaction/tools.go';

const src = fs.readFileSync(SRC, 'utf8');
let dst = fs.readFileSync(DST, 'utf8');
const eol = dst.includes('\r\n') ? '\r\n' : '\n';
const toEol = (s) => s.replace(/\r?\n/g, eol);

const SRC_START = '// emojiLikeIDs mirrors bot.emojiLikeIDs';
const END_MARK = '// signatureCooldown is the minimum gap';
const DST_START = '// EmojiLikeTool lets the persona';

const sA = src.indexOf(SRC_START);
const sB = src.indexOf(END_MARK);
if (sA < 0 || sB < 0 || sB <= sA) {
	throw new Error('source block not found');
}
const block = src.slice(sA, sB);

const dA = dst.indexOf(DST_START);
const dB = dst.indexOf(END_MARK);
if (dA < 0 || dB < 0 || dB <= dA) {
	throw new Error('destination block not found');
}
dst = dst.slice(0, dA) + toEol(block) + dst.slice(dB);

if (!/[\r\n]\t"strconv"/.test(dst)) {
	if (!/[\r\n]\t"fmt"/.test(dst)) {
		throw new Error('fmt import anchor not found');
	}
	dst = dst.replace(/([\r\n])\t"fmt"([\r\n])/, `$1\t"fmt"$2\t"strconv"$2`);
}

fs.writeFileSync(DST, dst);
console.log('mirrored emoji tool block + strconv import');
