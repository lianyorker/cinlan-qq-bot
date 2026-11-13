// Finish the core mirror: fix the test block (handlePlatformEvent) and
// drop the now-unused fmt import in service.go.
const fs = require('fs');
const rep = (file, o, n, tag) => {
	let t = fs.readFileSync(file, 'utf8');
	if (!t.includes(o)) throw new Error(tag + ' anchor miss in ' + file);
	fs.writeFileSync(file, t.replace(o, n));
	console.log(tag + ' ok');
};

const SVC = 'D:/Company/cinlan/apps/cinlan-bot/core/bot/service.go';
const TEST = 'D:/Company/cinlan/apps/cinlan-bot/core/bot/service_test.go';

rep(
	SVC,
	'\t"fmt"\n',
	'',
	'svc-fmt-import',
);

rep(
	TEST,
	'\n\t// Built-in commands ride through too — /清空 clears the session.\n' +
		'\tservice.handlePlatformEvent(context.Background(), testEvent("builtin-cmd", "/清空", false))\n' +
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
