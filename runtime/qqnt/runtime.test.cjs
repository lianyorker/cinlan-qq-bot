'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');

process.env.CINLAN_QQNT_TEST_EXPORTS = '1';
const { __test } = require('./runtime.cjs');

test('sendOnlineFile uses QQNT online-file element type', async () => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'cinlan-file-'));
  const filePath = path.join(directory, 'database.sql');
  fs.writeFileSync(filePath, 'select 1;');
  let sentElements;
  __test.state.msgService = {
    sendMsg(_clientID, _peer, elements) {
      sentElements = elements;
      return new Promise(() => {});
    },
    async getOnlineFileMsgs() {
      return {
        msgList: [{
          msgTime: String(Math.floor(Date.now() / 1000)),
          msgId: 'message-1',
          elements: sentElements,
        }],
      };
    },
  };

  try {
    const result = await __test.sendOnlineFile(
      { chatType: 1, peerUid: 'uid', guildId: '' },
      { type: 'file', data: { file: filePath, name: 'database.sql' } },
    );
    assert.equal(sentElements[0].elementType, 23);
    assert.equal(sentElements[0].fileElement.fileName, 'database.sql');
    assert.equal(result.message_id, 'message-1');
  } finally {
    fs.rmSync(directory, { recursive: true, force: true });
  }
});

test('buildAtElement resolves UIN to QQNT UID', async () => {
  __test.state.session = {
    getProfileService() {
      return {
        async getUidByUin(_source, uins) {
          return new Map([[uins[0], 'uid-20002']]);
        },
      };
    },
  };
  const element = await __test.buildAtElement(
    { type: 'at', data: { qq: '20002', name: '测试用户' } },
    { chatType: 2, peerUid: '30003', guildId: '' },
  );
  assert.equal(element.elementType, 1);
  assert.equal(element.textElement.atType, 2);
  assert.equal(element.textElement.atUid, '20002');
  assert.equal(element.textElement.atNtUid, 'uid-20002');
});

test('account switch refreshes self UID for inbound mention matching', async () => {
  __test.state.self = {
    uin: '10000002',
    uid: 'uid-old-account',
    nick: '旧账号',
  };
  __test.state.selfUIDLookupUin = '';
  __test.state.session = {
    getProfileService() {
      return {
        async getUidByUin(_source, uins) {
          return new Map([[uins[0], 'uid-current-account']]);
        },
      };
    },
  };

  __test.updateSelf({ uin: '10000001' });
  assert.equal(__test.state.self.uid, '');
  assert.equal(__test.state.self.nick, '');
  await new Promise((resolve) => setImmediate(resolve));

  assert.equal(__test.state.self.uid, 'uid-current-account');
  const chain = __test.convertElements([{
    elementType: 1,
    textElement: {
      content: '@机器人',
      atType: 2,
      atUid: 'uid-current-account',
      atNtUid: 'uid-current-account',
    },
  }, {
    elementType: 1,
    textElement: {
      content: ' 你好',
      atType: 0,
    },
  }]);
  assert.equal(chain[0].type, 'at');
  assert.equal(chain[0].data.qq, '10000001');
});

test('quick login applies the selected account identity', async () => {
  __test.state.self = { uin: '', uid: '', nick: '' };
  __test.state.loginService = {
    async getLoginList() {
      return {
        LocalLoginInfoList: [{
          uin: '10000001',
          uid: 'uid-current-account',
          nickName: '机器人',
          isQuickLogin: true,
        }],
      };
    },
    async quickLoginWithUin() {
      return { result: '0', loginErrorInfo: { errMsg: '' } };
    },
  };
  __test.state.wrapper = null;
  __test.state.session = null;
  __test.state.msgService = null;

  const result = await __test.quickLogin({ uin: '10000001' });

  assert.equal(result.requested_uin, '10000001');
  assert.deepEqual(__test.state.self, {
    uin: '10000001',
    uid: 'uid-current-account',
    nick: '机器人',
  });
});
