'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');

process.env.CINLAN_QQNT_TEST_EXPORTS = '1';
const { __test } = require('./runtime.cjs');

test('QR callbacks, refresh, expiry and ready never enter message channel', async () => {
  const previous = {loginConnected: __test.state.loginConnected,loginQR: __test.state.loginQR, loginService: __test.state.loginService, msgService: __test.state.msgService, socket: __test.state.socket, wrapper: __test.state.wrapper};
  const png = 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aS9sAAAAASUVORK5CYII=';
  const frames = []; let calls = 0;
  __test.state.msgService = null; __test.state.wrapper = {}; __test.state.loginQR = null;
  __test.state.loginConnected = true;
  __test.state.socket = { writable: true, write: s => frames.push(JSON.parse(s)) };
  __test.state.loginService = {getQRCodePicture: async () => {calls++; __test.recordLoginQR({pngBase64QrcodeData: png}); return {result: 0};}};
  try {
    assert.equal((await __test.getLoginQR({})).png_base64, png);
    await __test.getLoginQR({}); assert.equal(calls,1);
    await __test.getLoginQR({refresh:true}); assert.equal(calls,2);
    __test.state.loginQR.expires_at = new Date(Date.now()-1).toISOString();
    assert.equal(__test.loginQRStatus().status,'expired');
    assert.equal(__test.loginQRStatus().png_base64,undefined);
    await __test.getLoginQR({}); assert.equal(calls,3);
    __test.state.msgService = {};
    assert.equal((await __test.getLoginQR({})).status,'ready');
    assert.equal(__test.loginQRStatus().png_base64,undefined);
    assert.ok(frames.every(f=>f.type === 'login_qr' || f.type === 'runtime_status'));
    assert.throws(()=>__test.recordLoginQR({pngBase64QrcodeData:'evil'}),/PNG/);
  } finally { Object.assign(__test.state,previous); }
});

test('friend approval rejects errors and allows refusal without leaking to messages', async () => {
  const previous = __test.state.buddyService;
  __test.state.friendRequests.set('test',{uid:'u_test',reqTime:'1',flag:'test',at:Date.now()});
  try {
    __test.state.buddyService = {approvalFriendRequest:async ()=>({result:1,errMsg:'denied'})};
    await assert.rejects(__test.approveFriend({flag:'test',approve:true,remark:''}),/denied/);
    let got;
    __test.state.buddyService = {approvalFriendRequest:async p=>{got=p;return {result:0};}};
    await __test.approveFriend({flag:'test',approve:false,remark:''});
    assert.equal(got.accept,false);
  } finally {__test.state.buddyService=previous; __test.state.friendRequests.delete('test');}
});

test('action failures publish last_error and correlated failure result', async () => {
  const socket = __test.state.socket; const buddy = __test.state.buddyService;
  const frames=[];
  __test.state.socket={writable:true,write:s=>frames.push(JSON.parse(s))};
  __test.state.buddyService={};
  try {
    __test.handleEnvelope({v:1,type:'action',id:'test-id',payload:{name:'approve_friend',params:{flag:'unknown',approve:true,remark:''}}});
    await new Promise(resolve=>setImmediate(resolve));
    const result=frames.find(f=>f.type==='action_result');
    assert.equal(result.id,'test-id');assert.equal(result.ok,false);
    assert.ok(frames.some(f=>f.type==='runtime_status' && f.payload.last_error.includes('action_approve_friend')));
  } finally {__test.state.socket=socket;__test.state.buddyService=buddy;__test.state.errors.delete('action_approve_friend');}
});

test('headless login waits for connection and does not attach a cached identity', async () => {
  const previous = {wrapper:__test.state.wrapper,loginService:__test.state.loginService,self:__test.state.self,
    msgService:__test.state.msgService,autoLoginStarted:__test.state.autoLoginStarted,loginConfirmed:__test.state.loginConfirmed,loginConnected:__test.state.loginConnected};
  const oldEnv = process.env.CINLAN_QQNT_HEADLESS;
  const oldUin = process.env.QQNT_LOGIN_UIN;
  process.env.CINLAN_QQNT_HEADLESS='true';delete process.env.QQNT_LOGIN_UIN;
  __test.state.wrapper={};__test.state.self={uin:'',uid:'',nick:''};__test.state.msgService=null;
  __test.state.autoLoginStarted=false;__test.state.loginConfirmed=false;__test.state.loginConnected=false;
  let calls=0;
  __test.state.loginService={getLoginList:async()=>({LocalLoginInfoList:[{uin:'10001',isQuickLogin:true}]}),
    quickLoginWithUin:async()=>{calls++;return {result:0};}};
  try {
    await __test.discoverSelfFromLoginList(false);
    assert.equal(__test.state.self.uin,'');assert.equal(calls,0);
    await assert.rejects(__test.getLoginQR({}),/not connected/);
    __test.state.loginConnected=true;
    await __test.discoverSelfFromLoginList(true);
    assert.equal(calls,1);assert.equal(__test.state.loginConfirmed,true);
    assert.equal(__test.state.self.uin,'10001');
  } finally {
    Object.assign(__test.state,previous);
    if(oldEnv===undefined)delete process.env.CINLAN_QQNT_HEADLESS;else process.env.CINLAN_QQNT_HEADLESS=oldEnv;
    if(oldUin===undefined)delete process.env.QQNT_LOGIN_UIN;else process.env.QQNT_LOGIN_UIN=oldUin;
  }
});

test('friend callback is independent, incoming-only, deduplicated and approves observed flag', async () => {
  const previous = { session: __test.state.session, socket: __test.state.socket, buddyService: __test.state.buddyService };
  const frames = []; const approvals = [];
  __test.state.friendRequests.clear();
  __test.state.socket = { writable: true, write: frame => frames.push(JSON.parse(frame)) };
  __test.state.session = { getProfileService: () => ({getUinByUid: async () => new Map([['u_friend', '20002']])}) };
  __test.state.buddyService = { approvalFriendRequest: async request => { approvals.push(request); return {result: 0}; } };
  try {
    const req = { isInitiator: false, isDecide: false, friendUid: 'u_friend', reqTime: '123', extWords: 'hello', friendNick: 'tester' };
    await __test.handleFriendRequests({buddyReqs: [req, {...req, isInitiator: true}, {...req, isDecide: true}, {...req,isDoubt:true}]});
    await __test.handleFriendRequests({buddyReqs: [req]});
    assert.equal(frames.length, 1);
    assert.equal(frames[0].type, 'friend_request');
    assert.equal(frames[0].payload.uin, '20002');
    const flag = frames[0].payload.flag;
    await assert.rejects(__test.approveFriend({flag: 'unknown',approve:true,remark:''}), /unknown/);
    await __test.approveFriend({flag,approve:true,remark:''});
    assert.deepEqual(approvals, [{friendUid:'u_friend',accept:true,refuseMsg:'',reqTime:'123'}]);
    await assert.rejects(__test.approveFriend({flag,approve:true,remark:''}), /decided/);
  } finally { Object.assign(__test.state, previous); __test.state.friendRequests.clear(); }
});

test('convertElements keeps text whose QQNT at target is numeric zero', () => {
  const previousSelf = { ...__test.state.self };
  __test.state.self = {
    uin: '2740954283',
    uid: 'self-uid',
    nick: '七七',
  };
  try {
    assert.deepEqual(__test.convertElements([
      {
        textElement: {
          atType: 2,
          atUid: 'self-uid',
          atNtUid: 'self-uid',
          content: '@七七',
        },
      },
      {
        textElement: {
          atType: 0,
          atUid: 0,
          atNtUid: 0,
          atUin: 0,
          atNtUin: 0,
          content: '定时任务好像是redis 我有必要集成MQ吗',
        },
      },
    ]), [
      { type: 'at', data: { qq: '2740954283', name: '@七七' } },
      {
        type: 'text',
        data: { text: '定时任务好像是redis 我有必要集成MQ吗' },
      },
    ]);
  } finally {
    __test.state.self = previousSelf;
  }
});

test('handshake frames stay on the connecting socket', () => {
  const frames = [];
  const connectingSocket = {
    destroyed: false,
    writable: true,
    write(frame) {
      frames.push(JSON.parse(frame));
    },
  };
  const unrelatedSocket = {
    destroyed: false,
    writable: true,
    write() {
      assert.fail('frame was written to an unrelated socket');
    },
  };
  const previousSocket = __test.state.socket;
  __test.state.socket = unrelatedSocket;
  try {
    __test.sendEnvelope({ type: 'hello' }, connectingSocket);
    __test.publishStatus(undefined, connectingSocket);
    assert.deepEqual(frames.map((frame) => frame.type), [
      'hello',
      'runtime_status',
    ]);
  } finally {
    __test.state.socket = previousSocket;
  }
});

test('runtime status is not queued before the IPC handshake', () => {
  const frames = [];
  const socket = {
    connecting: true,
    destroyed: false,
    writable: true,
    write(frame) {
      frames.push(frame);
    },
  };
  const previousSocket = __test.state.socket;
  __test.state.socket = socket;
  try {
    assert.equal(__test.publishStatus(), false);
    assert.equal(frames.length, 0);
  } finally {
    __test.state.socket = previousSocket;
  }
});

test('unchanged runtime status is not published repeatedly', () => {
  const frames = [];
  const socket = {
    connecting: false,
    destroyed: false,
    writable: true,
    write(frame) {
      frames.push(frame);
    },
  };
  const previous = {
    socket: __test.state.socket,
    statusSocket: __test.state.statusSocket,
    statusFingerprint: __test.state.statusFingerprint,
  };
  __test.state.socket = socket;
  __test.state.statusSocket = null;
  __test.state.statusFingerprint = '';
  try {
    assert.equal(__test.publishStatus(), true);
    assert.equal(__test.publishStatus(), false);
    assert.equal(frames.length, 1);
  } finally {
    Object.assign(__test.state, previous);
  }
});

test('sendPrivateFile uses a normal file element and waits for send success', async () => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'cinlan-file-'));
  const filePath = path.join(directory, 'database.sql');
  fs.writeFileSync(filePath, 'select 1;');
  const previous = {
    msgService: __test.state.msgService,
    session: __test.state.session,
  };
  let sentElements;
  let sentPeer;
  __test.state.msgService = {
    async generateMsgUniqueId() {
      return 'correlation-1';
    },
    sendMsg(_clientID, peer, elements) {
      sentPeer = peer;
      sentElements = elements;
      setImmediate(() => {
        __test.observeSendUpdates([{
          guildId: peer.guildId,
          sendStatus: 2,
          msgId: 'message-1',
        }]);
      });
      return { result: 0 };
    },
  };
  __test.state.session = {
    getMSFService() {
      return { getServerTime: () => 123456 };
    },
  };

  try {
    const result = await __test.sendPrivateFile(
      { chatType: 1, peerUid: 'uid', guildId: '' },
      { type: 'file', data: { file: filePath, name: 'database.sql' } },
    );
    assert.equal(sentPeer.guildId, 'correlation-1');
    assert.equal(sentElements[0].elementType, 3);
    assert.equal(sentElements[0].fileElement.fileName, 'database.sql');
    assert.equal(result.message_id, 'message-1');
  } finally {
    Object.assign(__test.state, previous);
    fs.rmSync(directory, { recursive: true, force: true });
  }
});

test('sendPrivateFile rejects a failed QQNT send status', async () => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'cinlan-file-'));
  const filePath = path.join(directory, 'database.sql');
  fs.writeFileSync(filePath, 'select 1;');
  const previous = {
    msgService: __test.state.msgService,
    session: __test.state.session,
  };
  __test.state.msgService = {
    generateMsgUniqueId: async () => 'correlation-failed',
    sendMsg(_clientID, peer) {
      setImmediate(() => {
        __test.observeSendUpdates([{
          guildId: peer.guildId,
          sendStatus: 0,
        }]);
      });
      return { result: 0 };
    },
  };
  __test.state.session = {
    getMSFService() {
      return { getServerTime: () => 123456 };
    },
  };

  try {
    await assert.rejects(
      __test.sendPrivateFile(
        { chatType: 1, peerUid: 'uid', guildId: '' },
        { type: 'file', data: { file: filePath, name: 'database.sql' } },
      ),
      /reported a failed file message/,
    );
  } finally {
    Object.assign(__test.state, previous);
    fs.rmSync(directory, { recursive: true, force: true });
  }
});

test('buildImageElement copies an allowed PNG into the QQNT media cache', async () => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'cinlan-image-'));
  const cacheDirectory = fs.mkdtempSync(path.join(os.tmpdir(), 'cinlan-image-cache-'));
  const imagePath = path.join(directory, 'generated.png');
  const cachePath = path.join(cacheDirectory, 'generated.png');
  const png = Buffer.alloc(24);
  Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a])
    .copy(png, 0);
  png.writeUInt32BE(2, 16);
  png.writeUInt32BE(3, 20);
  fs.writeFileSync(imagePath, png);
  const previous = {
    msgService: __test.state.msgService,
    roots: process.env.CINLAN_QQNT_SEND_IMAGE_ROOTS,
    maximum: process.env.CINLAN_QQNT_SEND_IMAGE_MAX_BYTES,
  };
  process.env.CINLAN_QQNT_SEND_IMAGE_ROOTS = directory;
  process.env.CINLAN_QQNT_SEND_IMAGE_MAX_BYTES = '1024';
  __test.state.msgService = {
    getRichMediaFilePathForGuild() {
      return cachePath;
    },
  };

  try {
    const element = await __test.buildImageElement({
      type: 'image',
      data: { file: imagePath },
    });
    assert.equal(element.elementType, 2);
    assert.equal(element.picElement.picWidth, 2);
    assert.equal(element.picElement.picHeight, 3);
    assert.equal(element.picElement.picType, 1001);
    assert.deepEqual(fs.readFileSync(cachePath), png);
  } finally {
    __test.state.msgService = previous.msgService;
    if (previous.roots === undefined) {
      delete process.env.CINLAN_QQNT_SEND_IMAGE_ROOTS;
    } else {
      process.env.CINLAN_QQNT_SEND_IMAGE_ROOTS = previous.roots;
    }
    if (previous.maximum === undefined) {
      delete process.env.CINLAN_QQNT_SEND_IMAGE_MAX_BYTES;
    } else {
      process.env.CINLAN_QQNT_SEND_IMAGE_MAX_BYTES = previous.maximum;
    }
    fs.rmSync(directory, { recursive: true, force: true });
    fs.rmSync(cacheDirectory, { recursive: true, force: true });
  }
});

test('native image sending rejects files outside configured roots', () => {
  const allowed = fs.mkdtempSync(path.join(os.tmpdir(), 'cinlan-image-allowed-'));
  const outside = fs.mkdtempSync(path.join(os.tmpdir(), 'cinlan-image-outside-'));
  const imagePath = path.join(outside, 'generated.png');
  fs.writeFileSync(imagePath, Buffer.from([
    0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
    0, 0, 0, 0, 0, 0, 0, 0,
    0, 0, 0, 1, 0, 0, 0, 1,
  ]));
  const previous = process.env.CINLAN_QQNT_SEND_IMAGE_ROOTS;
  process.env.CINLAN_QQNT_SEND_IMAGE_ROOTS = allowed;
  try {
    assert.throws(
      () => __test.resolveAllowedSendImage(imagePath),
      /outside the allowed roots/,
    );
  } finally {
    if (previous === undefined) {
      delete process.env.CINLAN_QQNT_SEND_IMAGE_ROOTS;
    } else {
      process.env.CINLAN_QQNT_SEND_IMAGE_ROOTS = previous;
    }
    fs.rmSync(allowed, { recursive: true, force: true });
    fs.rmSync(outside, { recursive: true, force: true });
  }
});

test('getGroupMemberInfo returns only membership for the requested user', async () => {
  const previousSession = __test.state.session;
  const requests = [];
  __test.state.session = {
    getProfileService() {
      return {
        getUidByUin(_source, uins) {
          return new Map([[uins[0], 'uid-member']]);
        },
      };
    },
    getGroupService() {
      return {
        async getAllMemberList(groupID, force) {
          requests.push({ groupID, force });
          return {
            errCode: 0,
            errMsg: '',
            result: {
              infos: force
                ? new Map([['uid-member', { uid: 'uid-member', nick: 'secret' }]])
                : new Map(),
            },
          };
        },
      };
    },
  };

  try {
    const result = await __test.getGroupMemberInfo({
      group_id: '20000001',
      user_id: '10000002',
    });
    assert.deepEqual(result, {
      group_id: '20000001',
      user_id: '10000002',
      member: true,
    });
    assert.deepEqual(requests, [
      { groupID: '20000001', force: false },
      { groupID: '20000001', force: true },
    ]);
    assert.equal('infos' in result, false);
  } finally {
    __test.state.session = previousSession;
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

test('convertElements preserves mention targets when atType is unknown', () => {
  const chain = __test.convertElements([{
    elementType: 1,
    textElement: {
      content: '@其他机器人',
      atType: 0,
      atUid: 'uid-other-bot',
    },
  }]);
  assert.equal(chain.length, 1);
  assert.equal(chain[0].type, 'at');
  assert.equal(chain[0].data.qq, 'uid-other-bot');
  assert.equal(chain[0].data.name, '@其他机器人');
});

test('message session waits for a confirmed account identity', () => {
  const previous = {
    wrapper: __test.state.wrapper,
    loginService: __test.state.loginService,
    session: __test.state.session,
    msgService: __test.state.msgService,
    msgListenerID: __test.state.msgListenerID,
    self: __test.state.self,
  };
  let sessionLookups = 0;
  const msgService = {
    addKernelMsgListener() {
      return 1;
    },
  };
  __test.state.wrapper = {
    NodeIQQNTWrapperSession: {
      getNTWrapperSession() {
        sessionLookups += 1;
        return {
          getMsgService() {
            return msgService;
          },
        };
      },
    },
  };
  __test.state.loginService = {};
  __test.state.session = null;
  __test.state.msgService = null;
  __test.state.msgListenerID = null;
  __test.state.self = { uin: '', uid: '', nick: '' };

  try {
    __test.attachSession();
    assert.equal(sessionLookups, 0);
    assert.equal(__test.state.msgService, null);

    __test.state.self = {
      uin: '10000001',
      uid: 'uid-current-account',
      nick: '机器人',
    };
    __test.attachSession();
    assert.equal(sessionLookups, 1);
    assert.equal(__test.state.msgService, msgService);
  } finally {
    Object.assign(__test.state, previous);
  }
});

test('inbound image keeps local and remote references for multimodal input', () => {
  const chain = __test.convertElements([{
    elementType: 2,
    picElement: {
      filePath: 'D:\\qq-cache\\error.png',
      sourcePath: '',
      originImageUrl: 'https://example.test/error.png',
      md5HexStr: 'abc123',
    },
  }]);

  assert.equal(chain.length, 1);
  assert.equal(chain[0].type, 'image');
  assert.equal(chain[0].data.file_path, 'D:\\qq-cache\\error.png');
  assert.equal(chain[0].data.origin_image_url, 'https://example.test/error.png');
});

test('multi-forward chat records are converted to bounded prompt text', () => {
  const chain = __test.convertElements([{
    elementType: 16,
    elementId: 'forward-element-1',
    multiForwardMsgElement: {
      xmlContent: '<msg><title>群青日和</title><item>短效接码请求次数过多&amp;请换号</item></msg>',
    },
  }]);

  assert.equal(chain.length, 1);
  assert.equal(chain[0].type, 'text');
  assert.match(chain[0].data.text, /^\[聊天记录\]\n/);
  assert.match(chain[0].data.text, /短效接码请求次数过多&请换号/);
});

test('multi-forward chat records keep an opaque marker when no text is available', () => {
  const chain = __test.convertElements([{
    elementType: 16,
    elementId: 'forward-element-2',
    multiForwardMsgElement: {
      resId: 'opaque-forward-id',
    },
  }]);

  assert.deepEqual(chain, [{
    type: 'native_16',
    data: { element_id: 'forward-element-2' },
  }]);
});

test('inbound image is downloaded into the local QQ cache before delivery', async () => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'cinlan-inbound-image-'));
  const downloadedPath = path.join(directory, 'received.png');
  fs.writeFileSync(downloadedPath, Buffer.from([
    0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
  ]));
  const previousService = __test.state.msgService;
  let request;
  __test.state.msgService = {
    async getMsgsByMsgId() {
      return { msgList: [] };
    },
    downloadRichMedia(value) {
      request = value;
      setImmediate(() => {
        __test.observeRichMediaDownload([{
          msgId: value.msgId,
          msgElementId: value.elementId,
          filePath: downloadedPath,
        }]);
      });
    },
  };
  const raw = {
    chatType: 1,
    msgId: 'image-message-1',
    peerUid: 'uid-peer',
    elements: [{
      elementType: 2,
      elementId: 'image-element-1',
      picElement: {
        filePath: 'C:\\Users\\sender\\unavailable.png',
      },
    }],
  };

  try {
    const prepared = await __test.prepareInboundImages(raw);
    assert.equal(request.msgId, 'image-message-1');
    assert.equal(request.elementId, 'image-element-1');
    assert.equal(request.downloadType, 1);
    assert.equal(prepared.elements[0].picElement.filePath, downloadedPath);
    assert.equal(prepared.elements[0].picElement.sourcePath, downloadedPath);
  } finally {
    __test.state.msgService = previousService;
    fs.rmSync(directory, { recursive: true, force: true });
  }
});

test('inbound image uses the canonical stored element download identifiers', async () => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'cinlan-stored-image-'));
  const downloadedPath = path.join(directory, 'received.png');
  fs.writeFileSync(downloadedPath, Buffer.from([
    0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
  ]));
  const previousService = __test.state.msgService;
  const stored = {
    chatType: 2,
    msgId: 'image-message-2',
    peerUid: 'group-peer',
    elements: [{
      elementType: 2,
      elementId: 'stored-image-element',
      picElement: {},
    }],
  };
  __test.state.msgService = {
    async getMsgsByMsgId() {
      return { msgList: [stored] };
    },
    downloadRichMedia(value) {
      setImmediate(() => {
        __test.observeRichMediaDownload([{
          msgId: value.msgId,
          msgElementId: value.elementId,
          downloadedFilePath: downloadedPath,
        }]);
      });
    },
  };

  try {
    const prepared = await __test.prepareInboundImages({
      ...stored,
      elements: [{
        elementType: 2,
        elementId: '',
        picElement: {
          filePath: 'C:\\Users\\sender\\unavailable.png',
        },
      }],
    });
    assert.equal(prepared, stored);
    assert.equal(stored.elements[0].picElement.filePath, downloadedPath);
  } finally {
    __test.state.msgService = previousService;
    fs.rmSync(directory, { recursive: true, force: true });
  }
});

test('inbound PTT is transcribed before it reaches the message chain', async () => {
  const previousService = __test.state.msgService;
  const raw = {
    chatType: 1,
    msgId: 'voice-message-1',
    peerUid: 'uid-peer',
    guildId: '',
    elements: [{
      elementType: 4,
      pttElement: {
        fileName: 'voice.amr',
        filePath: 'D:\\qq-cache\\voice.amr',
      },
    }],
  };
  let translatedMessageID = '';
  let translatedCanonicalElement = false;
  let getMessageCalls = 0;
  __test.state.msgService = {
    async translatePtt2Text(messageID, _peer, element) {
      translatedMessageID = messageID;
      translatedCanonicalElement = element.pttElement.canonical === true;
    },
    async getMsgsByMsgId() {
      getMessageCalls += 1;
      return {
        msgList: [{
          ...raw,
          elements: [{
            ...raw.elements[0],
            pttElement: {
              ...raw.elements[0].pttElement,
              canonical: true,
              text: getMessageCalls > 1 ? 'hello from voice' : '',
            },
          }],
        }],
      };
    },
  };

  try {
    const translated = await __test.transcribePttMessage(raw);
    const chain = __test.convertElements(translated.elements);
    assert.equal(translatedMessageID, 'voice-message-1');
    assert.equal(translatedCanonicalElement, true);
    assert.equal(getMessageCalls, 2);
    assert.equal(chain[0].type, 'record');
    assert.equal(chain[0].data.text, 'hello from voice');
  } finally {
    __test.state.msgService = previousService;
  }
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

test('login listener starts configured quick login', async () => {
  const previous = {
    wrapper: __test.state.wrapper,
    loginService: __test.state.loginService,
    loginListenerID: __test.state.loginListenerID,
    loginConnected: __test.state.loginConnected,
    loginConfirmed: __test.state.loginConfirmed,
    autoLoginStarted: __test.state.autoLoginStarted,
    session: __test.state.session,
    msgService: __test.state.msgService,
    self: __test.state.self,
  };
  const previousUIN = process.env.QQNT_LOGIN_UIN;
  let quickLoginCalls = 0;
  let listener;
  const loginService = {
    addKernelLoginListener(value) {
      listener = value;
      return 1;
    },
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
      quickLoginCalls += 1;
      return { result: '0', loginErrorInfo: { errMsg: '' } };
    },
  };
  process.env.QQNT_LOGIN_UIN = '10000001';
  __test.state.wrapper = {
    NodeIKernelLoginService: {
      get() {
        return loginService;
      },
    },
  };
  __test.state.loginService = null;
  __test.state.loginListenerID = null;
  __test.state.autoLoginStarted = false;
  __test.state.session = null;
  __test.state.msgService = null;
  __test.state.self = { uin: '', uid: '', nick: '' };

  try {
    __test.attachLoginListener();
    await new Promise((resolve) => setImmediate(resolve));
    assert.equal(quickLoginCalls, 0);
    listener.onLoginConnected();
    await new Promise((resolve) => setImmediate(resolve));
    assert.equal(quickLoginCalls, 1);
    assert.equal(__test.state.self.uin, '10000001');
  } finally {
    if (previousUIN === undefined) {
      delete process.env.QQNT_LOGIN_UIN;
    } else {
      process.env.QQNT_LOGIN_UIN = previousUIN;
    }
    Object.assign(__test.state, previous);
  }
});

test('AVSDK listener publishes bounded summaries without raw buffers', () => {
  const previous = {
    socket: __test.state.socket,
    avService: __test.state.avService,
    avListenerID: __test.state.avListenerID,
    avMethods: __test.state.avMethods,
    avEventSequence: __test.state.avEventSequence,
    recentAVEvents: __test.state.recentAVEvents,
  };
  const frames = [];
  let listener;
  const service = {
    addKernelAVSDKListener(value) {
      listener = value;
      return 77;
    },
    removeKernelAVSDKListener() {},
    setActionFromAVSDK() {},
  };
  __test.state.socket = {
    connecting: false,
    destroyed: false,
    writable: true,
    write(frame) {
      frames.push(JSON.parse(frame));
    },
  };
  __test.state.avService = null;
  __test.state.avListenerID = null;
  __test.state.avMethods = [];
  __test.state.avEventSequence = 0;
  __test.state.recentAVEvents = [];

  try {
    __test.attachAVListener({
      getAVSDKService() {
        return service;
      },
    });
    assert.equal(__test.state.avListenerID, 77);
    assert.deepEqual(__test.state.avMethods, [
      'addKernelAVSDKListener',
      'removeKernelAVSDKListener',
      'setActionFromAVSDK',
    ]);

    listener.OnInviteActionToAVSDK(23, Buffer.from('abc'));
    const eventFrame = frames.find((frame) => frame.type === 'av_event');
    assert.ok(eventFrame);
    assert.equal(eventFrame.payload.callback, 'OnInviteActionToAVSDK');
    assert.equal(eventFrame.payload.action_code_candidate, 23);
    assert.equal(eventFrame.payload.arguments[0].number_value, 23);
    assert.equal(eventFrame.payload.arguments[1].byte_length, 3);
    assert.equal(
      eventFrame.payload.arguments[1].sha256,
      'ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad',
    );
    assert.equal(JSON.stringify(eventFrame).includes('abc'), false);

    listener.onActionToAVSDK(
      24,
      Buffer.alloc(256 * 1024 + 1, 0x61),
    );
    const recent = __test.inspectAVSDK().recent_events;
    const oversized = recent.at(-1).arguments[1];
    assert.equal(oversized.oversized, true);
    assert.equal(oversized.sha256, undefined);
  } finally {
    Object.assign(__test.state, previous);
  }
});

test('AVSDK inspection keeps only the 32 most recent events', () => {
  const previous = {
    socket: __test.state.socket,
    avEventSequence: __test.state.avEventSequence,
    recentAVEvents: __test.state.recentAVEvents,
  };
  __test.state.socket = null;
  __test.state.avEventSequence = 0;
  __test.state.recentAVEvents = [];
  try {
    for (let index = 0; index < 40; index += 1) {
      __test.recordAVEvent('onActionToAVSDK', [index]);
    }
    const recent = __test.inspectAVSDK().recent_events;
    assert.equal(recent.length, 32);
    assert.equal(recent[0].sequence, 9);
    assert.equal(recent.at(-1).sequence, 40);
  } finally {
    Object.assign(__test.state, previous);
  }
});
