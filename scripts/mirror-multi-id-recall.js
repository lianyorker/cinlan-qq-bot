// Mirror multi-id recall into cinlan-bot core:
// 1) platform.Event.MessageIDs
// 2) mergePlatformEvents accumulates ids; clonePlatformEvent deep-copies
// 3) stageModeration recalls every merged id
const fs = require('fs');
const rep = (file, o, n, tag) => {
	let t = fs.readFileSync(file, 'utf8');
	if (!t.includes(o)) throw new Error(tag + ' anchor miss in ' + file);
	fs.writeFileSync(file, t.replace(o, n));
	console.log(tag + ' ok');
};

const PLAT = 'D:/Company/cinlan/apps/cinlan-bot/core/platform/platform.go';
const BATCH = 'D:/Company/cinlan/apps/cinlan-bot/core/bot/batch.go';
const MOD = 'D:/Company/cinlan/apps/cinlan-bot/core/bot/moderation.go';

rep(
	PLAT,
	'\tMessageID   string `json:"message_id"`\n\tSelfID      string',
	'\tMessageID   string `json:"message_id"`\n' +
		'\t// MessageIDs accumulates every merged message\'s id when a batch run\n' +
		'\t// folds several posts into one event — MessageID alone points at the\n' +
		'\t// last post, which left earlier spam messages un-recallable.\n' +
		'\tMessageIDs  []string `json:"-"`\n\tSelfID      string',
	'plat-field',
);

rep(
	BATCH,
	'\tmerged.ID = current.ID\n\tmerged.MessageID = current.MessageID\n\tmerged.SubType',
	'\tmerged.ID = current.ID\n\tmerged.MessageID = current.MessageID\n' +
		'\tif len(merged.MessageIDs) == 0 && previous.MessageID != "" {\n' +
		'\t\tmerged.MessageIDs = []string{previous.MessageID}\n\t}\n' +
		'\tif current.MessageID != "" {\n' +
		'\t\tmerged.MessageIDs = append(merged.MessageIDs, current.MessageID)\n\t}\n' +
		'\tmerged.SubType',
	'merge-ids',
);

rep(
	BATCH,
	'\tevent.Chain = event.Chain.Clone()\n\tevent.Metadata = cloneMetadata(event.Metadata)',
	'\tevent.Chain = event.Chain.Clone()\n' +
		'\tevent.MessageIDs = append([]string(nil), event.MessageIDs...)\n' +
		'\tevent.Metadata = cloneMetadata(event.Metadata)',
	'clone-ids',
);

rep(
	MOD,
	'if state.messageID != "" {\n' +
		'\t\tif err := s.groupModerator.RecallMessage(\n' +
		'\t\t\tctx, state.selfID, state.messageID,\n\t\t); err != nil {\n' +
		'\t\t\ts.logger.Warn(\n\t\t\t\t"moderation recall failed",\n' +
		'\t\t\t\t"chat", state.chatID,\n\t\t\t\t"user", state.userID,\n' +
		'\t\t\t\t"message_id", state.messageID,\n\t\t\t\t"error", err.Error(),\n\t\t\t)\n' +
		'\t\t} else {\n\t\t\ts.feelAmbient(\n\t\t\t\tctx, state, "mod:recall", -4,\n' +
		'\t\t\t\t"撤回了 "+state.event.SenderName+" 发的广告，警告了他",\n' +
		'\t\t\t\t8*time.Minute,\n\t\t\t)\n\t\t}\n\t}',
	'recallIDs := state.event.MessageIDs\n' +
		'\tif len(recallIDs) == 0 && state.messageID != "" {\n' +
		'\t\trecallIDs = []string{state.messageID}\n\t}\n' +
		'\trecalled := false\n\tfor _, mid := range recallIDs {\n' +
		'\t\tif err := s.groupModerator.RecallMessage(\n' +
		'\t\t\tctx, state.selfID, mid,\n\t\t); err != nil {\n' +
		'\t\t\ts.logger.Warn(\n\t\t\t\t"moderation recall failed",\n' +
		'\t\t\t\t"chat", state.chatID,\n\t\t\t\t"user", state.userID,\n' +
		'\t\t\t\t"message_id", mid,\n\t\t\t\t"error", err.Error(),\n\t\t\t)\n' +
		'\t\t} else {\n\t\t\trecalled = true\n\t\t}\n\t}\n' +
		'\tif recalled {\n\t\ts.feelAmbient(\n\t\t\tctx, state, "mod:recall", -4,\n' +
		'\t\t\t"撤回了 "+state.event.SenderName+" 发的广告，警告了他",\n' +
		'\t\t\t8*time.Minute,\n\t\t)\n\t}',
	'mod-recall-all',
);
