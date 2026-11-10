// Fix the core mirror of replyStep: core's sendChains uses (ctx, route,
// userID, senderName, messageID, chains) with s.sendOutbound +
// conversations.RecordOutbound + routeBehavior delay, and pipeline has
// capReplyChains/limitReplyChains gating. Adapt all of it for steps.
const fs = require('fs');

const SVC = 'D:/Company/cinlan/apps/cinlan-bot/core/bot/service.go';
const PL = 'D:/Company/cinlan/apps/cinlan-bot/core/bot/pipeline.go';
const RP = 'D:/Company/cinlan/apps/cinlan-bot/core/bot/reply_policy.go';
const rep = (t, o, n, tag) => {
	if (!t.includes(o)) throw new Error(tag + ' anchor missing: ' + o.slice(0, 70));
	return t.split(o).join(n);
};

// --- 1: service.go — rewrite sendReplySteps with the core route signature ----
{
	let t = fs.readFileSync(SVC, 'utf8');
	const a = t.indexOf('// replyStep is one outbound beat');
	const b = t.indexOf('// noteSpoke stamps');
	if (a < 0 || b < 0) throw new Error('replyStep block missing in service.go');
	const block = `// replyStep is one outbound beat: a message chain to send, or an action
// to fire (poke, emoji reaction, send-then-recall). Actions carry
// position — a poke the model placed between two lines must land
// between those two bubbles, not before all of them.
type replyStep struct {
	chain  message.Chain
	action func()
}

// sendReplySteps walks the ordered beats: text/image chains send as
// bubbles with the usual delay/at/quote behaviour, action steps fire
// in place between them. Only chain steps count toward the delay and
// first-bubble quote/@ logic — a poke is instant, not typed.
func (s *Service) sendReplySteps(
	ctx context.Context,
	route route,
	userID, senderName, messageID string,
	steps []replyStep,
) bool {
	if s.sender == nil {
		s.stats.sendErrors.Add(1)
		s.logger.Error("failed to send reply", "reason", "sender_not_configured")
		return false
	}
	// Sulky or drowsy replies sit for a while before landing — a person
	// who isn't feeling it reads the @ and gets back to it when she gets
	// back to it. Zero for happy/neutral states so eagerness stays snappy.
	if len(steps) > 0 {
		if d := s.sulkDelay(route); d > 0 &&
			!waitForReplyPart(ctx, d) {
			return false
		}
	}
	index := 0
	for _, step := range steps {
		if step.action != nil {
			step.action()
			continue
		}
		if len(step.chain) == 0 {
			continue
		}
		if index > 0 {
			current, _ := step.chain.PlainText("")
			if !waitForReplyPart(ctx, s.replyPartDelay(current, s.routeBehavior(route))) {
				return false
			}
		}
		quote := s.cfg.QuoteReply && index == 0 && s.shouldQuoteReply(route.chatType)
		outbound := normalizeChainHTMLBreaks(step.chain.Clone())
		if s.cfg.GroupAtSender && route.chatType == platform.ChatGroup &&
			index == 0 && !quote {
			outbound = withSenderAt(outbound, userID, senderName)
		}
		err := s.sendOutbound(ctx, platform.Outbound{
			Platform:   route.platform,
			Connection: route.connection,
			ChatType:   route.chatType,
			ChatID:     route.chatID,
			SelfID:     route.selfID,
			ReplyTo:    messageID,
			Quote:      quote,
			Chain:      outbound,
		})
		if err != nil {
			s.stats.sendErrors.Add(1)
			s.logger.Error(
				"failed to send reply",
				"chat_type", route.chatType,
				"chat_id", route.chatID,
				"message_id", messageID,
				"part", index+1,
				"error", err,
			)
			return false
		}
		s.stats.replied.Add(1)
		if s.conversations != nil {
			plain, _ := outbound.PlainText("")
			if recErr := s.conversations.RecordOutbound(
				route.platform, route.connection, route.chatType, route.chatID,
				convstore.SenderAI, "", strings.TrimSpace(plain), s.now(),
			); recErr != nil {
				s.logger.Warn("conversation outbound record failed", "error", recErr)
			}
		}
		if index == 0 {
			s.noteSpoke(route.chatType, route.chatID)
		}
		index++
	}
	return true
}

`;
	t = t.slice(0, a) + block + t.slice(b);
	fs.writeFileSync(SVC, t);
	console.log('rewrote core/bot/service.go sendReplySteps');
}

// --- 2: reply_policy.go — capReplySteps/limitReplySteps -----------------------
{
	let t = fs.readFileSync(RP, 'utf8');
	if (!t.includes('capReplySteps')) {
		const add = `
// capReplySteps mirrors capReplyChains on the ordered beats: chain steps
// over the budget merge into the last in-budget bubble; action steps
// (poke/react/oops) keep their position — a poke is not a bubble.
func capReplySteps(steps []replyStep, budget int) []replyStep {
	if budget < 1 {
		budget = 1
	}
	var chainIdx []int
	for i, st := range steps {
		if st.action == nil {
			chainIdx = append(chainIdx, i)
		}
	}
	if len(chainIdx) <= budget {
		return steps
	}
	last := chainIdx[budget-1]
	tail := steps[last].chain.Clone()
	dropped := map[int]bool{}
	for _, ci := range chainIdx[budget:] {
		chain := steps[ci].chain
		if len(tail) > 0 && len(chain) > 0 &&
			tail[len(tail)-1].Type == message.TypeText &&
			chain[0].Type == message.TypeText {
			tail = append(tail, message.Text("\\n"))
		}
		tail = append(tail, chain...)
		dropped[ci] = true
	}
	steps[last].chain = tail
	out := make([]replyStep, 0, len(steps)-len(dropped))
	for i, st := range steps {
		if dropped[i] {
			continue
		}
		out = append(out, st)
	}
	return out
}

// limitReplySteps applies the per-bubble text cap to chain steps only.
func limitReplySteps(steps []replyStep, maxRunes int) []replyStep {
	for i, st := range steps {
		if st.action != nil {
			continue
		}
		steps[i].chain = limitReplyChains(
			[]message.Chain{st.chain}, maxRunes,
		)[0]
	}
	return steps
}
`;
		t += add;
		fs.writeFileSync(RP, t);
	}
	console.log('added capReplySteps/limitReplySteps to core/bot/reply_policy.go');
}

// --- 3: pipeline.go — swap the cap/limit + actual block for steps -------------
{
	let t = fs.readFileSync(PL, 'utf8');
	t = rep(t,
		'\t\tchains = limitReplyChains(capReplyChains(chains, maxParts), s.cfg.MaxReplyRunes)\n\t\tactual := make([]string, 0, len(chains))\n\t\tfor _, out := range chains {\n\t\t\tplain, _ := out.PlainText("")\n\t\t\tactual = append(actual, plain)\n\t\t}',
		'\t\tsteps = limitReplySteps(capReplySteps(steps, maxParts), s.cfg.MaxReplyRunes)\n\t\tactual := make([]string, 0, len(steps))\n\t\tfor _, st := range steps {\n\t\t\tif st.action != nil {\n\t\t\t\tcontinue\n\t\t\t}\n\t\t\tplain, _ := st.chain.PlainText("")\n\t\t\tactual = append(actual, plain)\n\t\t}', 'cap/limit');
	fs.writeFileSync(PL, t);
	console.log('patched core/bot/pipeline.go cap/limit block');
}
console.log('fix done');
