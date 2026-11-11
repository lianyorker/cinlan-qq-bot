// Mirror the notifyOff hard-silence fix into cinlan-bot core:
// /关闭通知 must silence follow-up and game continuations too —
// only real commands and @ mentions pass.
const fs = require('fs');
const P = 'D:/Company/cinlan/apps/cinlan-bot/core/bot/pipeline.go';
let t = fs.readFileSync(P, 'utf8');
const rep = (o, n, tag) => {
	if (!t.includes(o)) throw new Error(tag + ' anchor miss');
	t = t.split(o).join(n);
};

rep(
	'eventMentioned bool\n\tsessionID      string',
	`eventMentioned bool
	// notifyOff records that MentionOnly came from an operator's
	// /关闭通知 rather than the binding's own policy — the admin asked
	// for hard silence, so follow-up and game continuations stop
	// reaching her too; only real commands and @ mentions pass.
	notifyOff      bool
	sessionID      string`,
	'state',
);

rep(
	`!s.chatPrefs.NotifyEnabled(state.platform, state.selfID, state.chatType, state.chatID) &&
		(state.replyPolicy.Mode == binding.ReplyModeAIDecide ||
			state.replyPolicy.Mode == binding.ReplyModeAlways) {
		policy := state.replyPolicy
		policy.Mode = binding.ReplyModeMentionOnly
		state.replyPolicy = policy
	}`,
	`!s.chatPrefs.NotifyEnabled(state.platform, state.selfID, state.chatType, state.chatID) {
		state.notifyOff = true
		if state.replyPolicy.Mode == binding.ReplyModeAIDecide ||
			state.replyPolicy.Mode == binding.ReplyModeAlways {
			policy := state.replyPolicy
			policy.Mode = binding.ReplyModeMentionOnly
			state.replyPolicy = policy
		}
	}`,
	'gate',
);

rep(
	`slashJunk := strings.HasPrefix(strings.TrimSpace(state.text), "/")
		if !state.eventMentioned && !s.commandMessage(state.text) &&
			(slashJunk ||
				(!s.pendingGameMove(state) &&
					!s.pendingFollowUp(state))) {`,
	`slashJunk := strings.HasPrefix(strings.TrimSpace(state.text), "/")
		followUpOK := !state.notifyOff &&
			(s.pendingGameMove(state) || s.pendingFollowUp(state))
		if !state.eventMentioned && !s.commandMessage(state.text) &&
			(slashJunk || !followUpOK) {`,
	'mention',
);

fs.writeFileSync(P, t);
console.log('core patched');
