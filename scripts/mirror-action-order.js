// Mirror replyStep ordered-outbound refactor into cinlan-bot core:
// 1. core/bot/service.go   — sendChains -> sendReplySteps + replyStep type
// 2. core/bot/pipeline.go  — chains -> steps accumulation + action closures
const fs = require('fs');
const path = require('path');

const SRC_SVC = path.resolve(__dirname, '../internal/bot/service.go');
const DST_SVC = 'D:/Company/cinlan/apps/cinlan-bot/core/bot/service.go';
const DST_PL = 'D:/Company/cinlan/apps/cinlan-bot/core/bot/pipeline.go';
const rep = (t, o, n, tag) => {
	if (!t.includes(o)) throw new Error(tag + ' anchor missing: ' + o.slice(0, 60));
	if (!t.includes(n)) t = t.split(o).join(n);
	return t;
};

// --- 1: service.go — swap the whole sendChains block for replyStep+sendReplySteps
{
	const src = fs.readFileSync(SRC_SVC, 'utf8');
	let dst = fs.readFileSync(DST_SVC, 'utf8');
	const sA = src.indexOf('// replyStep is one outbound beat');
	const sB = src.indexOf('// noteSpoke stamps');
	if (sA < 0 || sB < 0) throw new Error('src replyStep block missing');
	const newBlock = src.slice(sA, sB);
	const dA = dst.indexOf('// sendChains sends');
	const dB = dst.indexOf('// noteSpoke stamps');
	if (dA < 0 || dB < 0) throw new Error('dst sendChains block missing');
	if (!dst.includes('sendReplySteps')) {
		dst = dst.slice(0, dA) + newBlock + dst.slice(dB);
		fs.writeFileSync(DST_SVC, dst);
	}
	console.log('patched core/bot/service.go');
}

// --- 2: pipeline.go — chains -> steps, deferred actions
{
	let t = fs.readFileSync(DST_PL, 'utf8');
	if (!t.includes('sendReplySteps')) {
		t = rep(t,
			'\t\tchains := make([]message.Chain, 0, len(parts))',
			'\t\tsteps := make([]replyStep, 0, len(parts))', 'decl');

		// three image-chain appends share the same shape — blanket swap
		t = rep(t, 'chains = append(\n\t\t\t\t\tchains, message.Chain{message.Image(path)},\n\t\t\t\t)',
			'steps = append(steps, replyStep{\n\t\t\t\t\tchain: message.Chain{message.Image(path)},\n\t\t\t\t})', 'img');

		t = rep(t,
			'\t\t\tif key, ok := parseReactMarker(part); ok && s.markerOn(state, "react") {\n\t\t\t\ts.reactToMessage(ctx, state, key)\n\t\t\t\tcontinue\n\t\t\t}\n\t\t\tif target, ok := parsePokeMarker(part); ok && s.markerOn(state, "poke") {\n\t\t\t\ts.sendPoke(ctx, state, target)\n\t\t\t\tcontinue\n\t\t\t}',
			'\t\t\tif key, ok := parseReactMarker(part); ok && s.markerOn(state, "react") {\n\t\t\t\tsteps = append(steps, replyStep{action: func() {\n\t\t\t\t\ts.reactToMessage(ctx, state, key)\n\t\t\t\t}})\n\t\t\t\tcontinue\n\t\t\t}\n\t\t\tif target, ok := parsePokeMarker(part); ok && s.markerOn(state, "poke") {\n\t\t\t\tsteps = append(steps, replyStep{action: func() {\n\t\t\t\t\ts.sendPoke(ctx, state, target)\n\t\t\t\t}})\n\t\t\t\tcontinue\n\t\t\t}', 'react+poke');

		t = rep(t,
			'chains = append(chains, message.Chain{\n\t\t\t\t\tmessage.Segment(message.TypeDice, nil),\n\t\t\t\t})',
			'steps = append(steps, replyStep{chain: message.Chain{\n\t\t\t\t\tmessage.Segment(message.TypeDice, nil),\n\t\t\t\t}})', 'dice');
		t = rep(t,
			'chains = append(chains, message.Chain{\n\t\t\t\t\tmessage.Segment(message.TypeRPS, nil),\n\t\t\t\t})',
			'steps = append(steps, replyStep{chain: message.Chain{\n\t\t\t\t\tmessage.Segment(message.TypeRPS, nil),\n\t\t\t\t}})', 'rps');

		t = rep(t,
			'\t\t\t\tif inner != "" {\n\t\t\t\t\ts.sendThenRecall(ctx, state, inner)',
			'\t\t\t\tif inner != "" {\n\t\t\t\t\tsteps = append(steps, replyStep{action: func() {\n\t\t\t\t\t\ts.sendThenRecall(ctx, state, inner)\n\t\t\t\t\t}})', 'oops');

		t = rep(t,
			'\t\t\tchains = append(chains, chain)',
			'\t\t\tsteps = append(steps, replyStep{chain: chain})', 'text');
		t = rep(t,
			'\t\tif len(chains) == 0 && skippedMarkers > 0 {',
			'\t\tif len(steps) == 0 && skippedMarkers > 0 {', 'empty-check');
		t = rep(t,
			'chains = append(chains, message.Chain{message.Text(fallback)})',
			'steps = append(steps, replyStep{\n\t\t\t\tchain: message.Chain{message.Text(fallback)},\n\t\t\t})', 'fallback');
		t = rep(t, '\t\t\tchains,\n\t\t) {', '\t\t\tsteps,\n\t\t) {', 'send-arg');
		t = rep(t, 's.sendChains(', 's.sendReplySteps(', 'send-name');

		fs.writeFileSync(DST_PL, t);
	}
	console.log('patched core/bot/pipeline.go');
}
console.log('mirror done');
