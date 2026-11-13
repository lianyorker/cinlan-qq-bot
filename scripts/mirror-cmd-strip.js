// Mirror the slash-command surface strip into cinlan-bot core:
// 1) handleCommand → registry-only stub (core signature: route route)
// 2) commandMessage → registry-only check
// 3) drop the /清空 built-in assertion in the mention-only test
const fs = require('fs');
const rep = (file, o, n, tag) => {
	let t = fs.readFileSync(file, 'utf8');
	if (!t.includes(o)) throw new Error(tag + ' anchor miss in ' + file);
	fs.writeFileSync(file, t.replace(o, n));
	console.log(tag + ' ok');
};

const SVC = 'D:/Company/cinlan/apps/cinlan-bot/core/bot/service.go';
const PIPE = 'D:/Company/cinlan/apps/cinlan-bot/core/bot/pipeline.go';
const TEST = 'D:/Company/cinlan/apps/cinlan-bot/core/bot/service_test.go';

const oldBody = `func (s *Service) handleCommand(
	ctx context.Context,
	sessionID string, route route,
	userID, senderName, messageID, text string,
) bool {
	command := strings.ToLower(strings.TrimSpace(text))
	var reply string

	switch command {
	case "/clear", "/清空":
		s.sessions.Clear(sessionID)
		reply = s.cfg.ClearReply
	case "/亲密度", "/affinity":
		if s.affinity == nil {
			reply = "亲密度功能未开启。"
			break
		}
		score := s.affinity.Score(route.platform, route.selfID, userID)
		level, percent := affinity.Level(score), affinity.Percent(score)
		if route.chatType == platform.ChatPrivate {
			level, percent = affinity.LevelIntimate, 100
		}
		reply = fmt.Sprintf("你和我的亲密度：%s（%d/100）", level, percent)
	case "/help", "/帮助":
		reply = "支持命令：/清空、/亲密度、/帮助"
	default:
		return false
	}

	s.stats.processed.Add(1)
	s.sendReply(ctx, route, userID, senderName, messageID, reply)
	return true
}`;
const newBody = `func (s *Service) handleCommand(
	ctx context.Context,
	sessionID string, route route,
	userID, senderName, messageID, text string,
) bool {
	// Built-in slash commands were removed — the shipped surface is the
	// notify pair registered by the host app; anything else is handled
	// by the command registry before this runs.
	return false
}`;
rep(SVC, oldBody, newBody, 'svc-handle');

rep(
	PIPE,
	'if s.commands != nil && s.commands.Matches(trimmed) {\n\t\treturn true\n\t}\n' +
		'\tswitch strings.ToLower(trimmed) {\n' +
		'\tcase "/clear", "/清空", "/亲密度", "/affinity", "/help", "/帮助":\n' +
		'\t\treturn true\n\t}\n\treturn false',
	'return s.commands != nil && s.commands.Matches(trimmed)',
	'pipe-cmdmsg',
);

rep(
	TEST,
	'\n\t// Built-in commands ride through too — /清空 clears the session.\n' +
		'\tservice.handleEvent(context.Background(), testEvent("builtin-cmd", "/清空", false))\n' +
		'\tif len(sender.messages) != 2 || sender.messages[1].text != "clear" {\n' +
		'\t\tt.Fatalf("builtin command dropped under mention-only: %#v", sender.messages)\n\t}\n',
	'',
	'test-builtin',
);
rep(
	TEST,
	'testEvent("slash-junk", "/notacommand", false))\n' +
		'\tif len(sender.messages) != 2 ||',
	'testEvent("slash-junk", "/notacommand", false))\n' +
		'\tif len(sender.messages) != 1 ||',
	'test-count',
);
