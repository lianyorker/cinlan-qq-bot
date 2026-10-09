'use strict';

const net = require('node:net');
const fs = require('node:fs');
const path = require('node:path');
const crypto = require('node:crypto');

const PROTOCOL_VERSION = 1;
const DEFAULT_MAX_FRAME_BYTES = 1024 * 1024;
const MAX_AV_BUFFER_BYTES = 256 * 1024;
const MAX_AV_ARGUMENTS = 8;
const MAX_AV_OBJECT_KEYS = 16;
const MAX_RECENT_AV_EVENTS = 32;
const PTT_TRANSCRIPTION_POLL_ATTEMPTS = 8;
const PTT_TRANSCRIPTION_POLL_DELAY_MS = 250;
const RICH_MEDIA_DOWNLOAD_TIMEOUT_MS = 15000;
const FILE_SEND_CONFIRM_TIMEOUT_MS = 30000;
const MAX_FRAME_BYTES = parseFrameLimit(
  process.env.CINLAN_QQNT_MAX_FRAME_BYTES,
);
const ACTION_TIMEOUT_MS = Number(process.env.CINLAN_QQNT_ACTION_TIMEOUT_MS) || 10000;
const AVSDK_CALLBACK_NAMES = [
  'onActionToAVSDK',
  'onS2CActionToAVSDK',
  'OnGroupVideoActionToAVSDK',
  'OnInviteActionToAVSDK',
  'OnGroupVideoServerPushToAVSDK',
];
const AVSDK_METHOD_NAMES = [
  'addKernelAVSDKListener',
  'removeKernelAVSDKListener',
  'sendGroupVideoJsonBuffer',
  'setActionFromAVSDK',
  'startGroupVideoCmdRequestFromAVSDK',
];
const CHAT_PRIVATE = 1;
const CHAT_GROUP = 2;
const ELEMENT_TEXT = 1;
const ELEMENT_PIC = 2;
const ELEMENT_FILE = 3;
const ELEMENT_PTT = 4;
const ELEMENT_VIDEO = 5;
const ELEMENT_FACE = 6;
const ELEMENT_REPLY = 7;
const ELEMENT_ARK = 10;
const ELEMENT_MARKET_FACE = 11;
const ELEMENT_MARKDOWN = 14;
const ELEMENT_MULTI_FORWARD = 16;
const ELEMENT_ONLINE_FILE = 23;
const SEND_STATUS_FAILED = 0;
const SEND_STATUS_SUCCESS = 2;
const AT_UNKNOWN = 0;
const AT_ALL = 1;
const AT_ONE = 2;
const PIC_TYPE_JPEG = 1000;
const PIC_TYPE_PNG = 1001;
const PIC_TYPE_GIF = 2000;
const DEFAULT_SEND_IMAGE_MAX_BYTES = 20 * 1024 * 1024;

const state = {
  started: false,
  socket: null,
  readBuffer: '',
  reconnectDelay: 500,
  statusSocket: null,
  statusFingerprint: '',
  wrapper: null,
  loginService: null,
  loginListenerID: null,
  loginConnected: false,
  loginConfirmed: false,
  autoLoginStarted: false,
  loginQR: null,
  qrRequest: null,
  qrResolve: null,
  qrReject: null,
  session: null,
  msgService: null,
  msgListenerID: null,
  avService: null,
  buddyService: null,
  buddyListenerID: null,
  friendQueue: Promise.resolve(),
  friendQueued: 0,
  friendRequests: new Map(),
  sessionGeneration: 0,
  avListenerID: null,
  avMethods: [],
  avEventSequence: 0,
  recentAVEvents: [],
  attachTimer: null,
  bootTime: 0,
  errors: new Map(),
  self: {
    uin: process.env.QQ_SELF_ID || '',
    uid: process.env.QQ_SELF_UID || '',
    nick: '',
  },
  selfUIDLookupUin: '',
  recentMessages: new Map(),
  pendingSends: new Map(),
  pendingMediaDownloads: new Map(),
  messageQueue: Promise.resolve(),
};

async function start() {
  if (state.started) {
    return;
  }
  state.started = true;
  state.bootTime = Date.now() / 1000;
  connectIPC();
  try { await loadWrapper(); } catch (error) { reportRuntimeError('wrapper_load', error); return; }
  attachLoginListener();
  state.attachTimer = setInterval(attachSession, 1000);
  state.attachTimer.unref?.();
  attachSession();
}

function connectIPC() {
  const address = process.env.CINLAN_QQNT_IPC_ADDR || '';
  const token = process.env.CINLAN_QQNT_IPC_TOKEN || '';
  const { host, port } = parseIPCAddress(address);
  if (!host || !Number.isInteger(port) || port < 1 || port > 65535 || !token) {
    throw new Error('invalid CINLAN_QQNT_IPC_ADDR or CINLAN_QQNT_IPC_TOKEN');
  }

  const socket = net.createConnection({ host, port });
  state.socket = socket;
  socket.setNoDelay(true);
  socket.setKeepAlive(true, 5000);
  socket.setEncoding('utf8');

  socket.on('connect', () => {
    state.reconnectDelay = 500;
    sendEnvelope({
      type: 'hello',
      token,
      payload: {
        runtime: 'cinlan-qqnt',
        pid: process.pid,
        qq_version: process.env.CINLAN_QQNT_VERSION || '',
        capabilities: [
          'message_event',
          'send_message',
          'send_file',
          ...(imageSendRoots().length > 0 ? ['send_image'] : []),
          'group_member_query',
          'runtime_status',
          'av_event',
          'inspect_avsdk',
          'friend_request',
          'approve_friend',
          'login_qr',
          'get_login_qr',
        ],
      },
    }, socket);
    publishStatus(undefined, socket);
  });
  socket.on('data', consumeData);
  socket.on('error', (error) => {
    console.error('[Cinlan QQ Runtime] IPC error:', error.message);
  });
  socket.on('close', () => {
    if (state.socket === socket) {
      state.socket = null;
      state.readBuffer = '';
      state.statusSocket = null;
      state.statusFingerprint = '';
    }
    const delay = state.reconnectDelay;
    state.reconnectDelay = Math.min(delay * 2, 10000);
    const timer = setTimeout(connectIPC, delay);
    timer.unref?.();
  });
}

function consumeData(chunk) {
  state.readBuffer += chunk;

  for (;;) {
    const newline = state.readBuffer.indexOf('\n');
    if (newline < 0) {
      if (Buffer.byteLength(state.readBuffer, 'utf8') + 1 > MAX_FRAME_BYTES) {
        state.socket?.destroy(new Error('IPC frame exceeds limit'));
      }
      return;
    }
    const line = state.readBuffer.slice(0, newline).replace(/\r$/, '');
    state.readBuffer = state.readBuffer.slice(newline + 1);
    if (!line) {
      continue;
    }
    if (Buffer.byteLength(line, 'utf8') + 1 > MAX_FRAME_BYTES) {
      state.socket?.destroy(new Error('IPC frame exceeds limit'));
      return;
    }
    let envelope;
    try {
      envelope = JSON.parse(line);
    } catch {
      state.socket?.destroy(new Error('invalid IPC JSON'));
      return;
    }
    handleEnvelope(envelope);
  }
}

function handleEnvelope(envelope) {
  if (!envelope || envelope.v !== PROTOCOL_VERSION) {
    return;
  }
  if (envelope.type === 'action' && typeof envelope.id === 'string') {
    let actionTimer;
    const actionTimeout = new Promise((_resolve,reject) => {
      actionTimer = setTimeout(() => reject(new Error('native action timeout')), ACTION_TIMEOUT_MS);
    });
    Promise.race([Promise.resolve(handleAction(envelope.payload || {})), actionTimeout])
      .then((payload) => {
        clearRuntimeError(`action_${stringValue(envelope.payload?.name)}`);
        sendEnvelope({
          type: 'action_result',
          id: envelope.id,
          ok: true,
          payload,
        });
      })
      .catch((error) => {
        reportRuntimeError(`action_${stringValue(envelope.payload?.name)}`, error);
        sendEnvelope({
          type: 'action_result',
          id: envelope.id,
          ok: false,
          error: error instanceof Error ? error.message : String(error),
        });
      }).finally(() => clearTimeout(actionTimer));
  }
}

async function loadWrapper() {
  const wrapperPath = process.env.CINLAN_QQNT_WRAPPER_PATH;
  if (!wrapperPath) {
    throw new Error('CINLAN_QQNT_WRAPPER_PATH is required');
  }
  const resolved = path.resolve(wrapperPath);
  try {
    state.wrapper = require(resolved);
  } catch {
    const nativeModule = { exports: {} };
    process.dlopen(nativeModule, resolved);
    state.wrapper = nativeModule.exports;
  }
  publishStatus('wrapper_loaded');
}

function attachLoginListener() {
  if (!state.wrapper?.NodeIKernelLoginService || state.loginService) {
    return;
  }
  try {
    const service = state.wrapper.NodeIKernelLoginService.get();
    if (!service) {
      return;
    }
    const listener = listenerProxy({
      onQRCodeGetPicture: (info) => {
        try { recordLoginQR(info); } catch (error) { reportRuntimeError('login_qr', error); }
      },
      onQRCodeLoginSucceed: (info) => {
        const uin = stringValue(info?.uin || info?.account);
        const configured = stringValue(process.env.QQNT_LOGIN_UIN);
        if (!uin || (configured && configured !== uin)) {
          reportRuntimeError('login_identity', new Error('QR login account does not match configured UIN'));
          return;
        }
        state.loginConfirmed = true;
        clearRuntimeError('login_identity');
        state.loginQR = null;
        clearRuntimeError('login_qr');
        clearRuntimeError('login_discovery');
        updateSelf(info);
        attachSession();
        publishLoginQR();
      },
      onQRCodeSessionFailed: (...args) => {
        state.loginQR = null;
        state.qrReject?.(new Error('QQNT QR session failed'));
        reportRuntimeError('login_qr', new Error(`QQNT QR session failed: ${args.map(stringValue).join(',')}`));
        publishLoginQR();
      },
      onQRCodeSessionQuickLoginFailed: (...args) => {
        state.loginConfirmed = false;
        state.loginQR = null;
        state.qrReject?.(new Error('QQNT QR quick login failed'));
        reportRuntimeError('login_qr', new Error(`QQNT QR quick login failed: ${args.map(stringValue).join(',')}`));
        publishLoginQR();
      },
      onLoginConnected: () => {
        state.loginConnected = true;
        void discoverSelfFromLoginList(true);
      },
      onLoginState: (...args) => findAndUpdateSelf(args),
      onLoginRecordUpdate: (...args) => findAndUpdateSelf(args),
      onUserLoggedIn: (uin) => {
        if (uin !== undefined && uin !== null) {
          state.loginConfirmed = true;
          updateSelf({ uin: String(uin) });
        }
      },
      onLogoutSucceed: () => {
        state.self = { uin: '', uid: '', nick: '' };
        state.loginConfirmed = false;
        resetSession();
        state.loginQR = null;
        state.autoLoginStarted = false;
        publishLoginQR();
        publishStatus('waiting_login');
        if (process.env.CINLAN_QQNT_HEADLESS === 'true') void discoverSelfFromLoginList(true);
      },
    });
    state.loginService = service;
    state.loginListenerID = service.addKernelLoginListener(listener);
    clearRuntimeError('login_attach');
    void discoverSelfFromLoginList(false);
  } catch (error) {
    state.loginService = null;
    reportRuntimeError('login_attach', error);
  }
}

async function discoverSelfFromLoginList(allowConfiguredLogin = false) {
  try {
    const result = await state.loginService?.getLoginList?.();
    clearRuntimeError('login_discovery');
    const entries = Array.isArray(result?.LocalLoginInfoList)
      ? result.LocalLoginInfoList
      : [];
    const targetUin = stringValue(process.env.QQNT_LOGIN_UIN);
    if (targetUin) {
      const target = entries.find(
        (entry) => stringValue(entry?.uin) === targetUin,
      );
      if (!target) {
        throw new Error(`configured QQ ${targetUin} is not in the local login list`);
      }
      if (!target.isQuickLogin) {
        throw new Error(`configured QQ ${targetUin} does not support quick login`);
      }
      if (process.env.CINLAN_QQNT_HEADLESS === 'true' && !allowConfiguredLogin) return;
      if (state.self.uin === targetUin && state.msgService) {
        updateSelf(target);
        return;
      }
      if (allowConfiguredLogin && !state.autoLoginStarted) {
        state.autoLoginStarted = true;
        await quickLogin({ uin: targetUin });
      }
      return;
    }
    const automatic = entries.filter((entry) => entry?.isAutoLogin);
    if (process.env.CINLAN_QQNT_HEADLESS === 'true' && !allowConfiguredLogin) return;
    if (process.env.CINLAN_QQNT_HEADLESS === 'true' && state.autoLoginStarted) return;
    if (process.env.CINLAN_QQNT_HEADLESS === 'true' && allowConfiguredLogin && !state.autoLoginStarted) {
      const candidates = automatic.length === 1 ? automatic : entries;
      if (candidates.length === 1 && candidates[0].isQuickLogin) {
        state.autoLoginStarted = true;
        await quickLogin({uin: stringValue(candidates[0].uin)});
        return;
      }
      await getLoginQR({});
      return;
    }
    if (automatic.length === 1) {
      updateSelf(automatic[0]);
    } else if (entries.length === 1) {
      updateSelf(entries[0]);
    }
  } catch (error) {
    reportRuntimeError('login_discovery', error);
    if (process.env.CINLAN_QQNT_HEADLESS === 'true' && state.loginConnected && !state.msgService) {
      void getLoginQR({}).catch(error => reportRuntimeError('login_qr', error));
    }
  }
  if (process.env.CINLAN_QQNT_HEADLESS === 'true' && state.loginConnected && !state.self.uin) {
    void getLoginQR({}).catch(error => reportRuntimeError('login_qr', error));
  }
}

function resetSession() {
  state.sessionGeneration++;
  for (const [service, method, id] of [
    [state.msgService, 'removeKernelMsgListener', state.msgListenerID],
    [state.avService, 'removeKernelAVSDKListener', state.avListenerID],
    [state.buddyService, 'removeKernelBuddyListener', state.buddyListenerID],
  ]) {
    try { if (id !== null) service?.[method]?.(id); } catch (error) { reportRuntimeError('session_detach', error); }
  }
  state.session = null; state.msgService = null; state.msgListenerID = null;
  state.avService = null; state.avListenerID = null; state.avMethods = [];
  state.buddyService = null; state.buddyListenerID = null;
  state.friendRequests.clear(); state.recentMessages.clear();
}

function recordLoginQR(info) {
  const png = info?.pngBase64QrcodeData;
  if (typeof png !== 'string' || png.length > Math.min(512*1024, MAX_FRAME_BYTES/2) ||
      !/^[A-Za-z0-9+/]+={0,2}$/.test(png) || !Buffer.from(png,'base64').subarray(0,8).equals(Buffer.from([137,80,78,71,13,10,26,10]))) {
    throw new Error('QQNT QR callback does not contain a bounded PNG base64');
  }
  if (runtimeState() === 'ready') return;
  state.loginQR = { png_base64: png, expires_at: new Date(Date.now()+60000).toISOString() };
  state.qrResolve?.();
  clearRuntimeError('login_qr');
  publishLoginQR();
}
function loginQRStatus() {
  const expired = state.loginQR && Date.parse(state.loginQR.expires_at) <= Date.now();
  return { state: runtimeState(), status: state.msgService ? 'ready' : (expired ? 'expired' : state.loginQR ? 'available' : 'unavailable'),
    ...(state.msgService || expired ? {} : state.loginQR || {}), last_error: latestRuntimeError() };
}
function publishLoginQR() { sendEnvelope({type:'login_qr',payload:loginQRStatus()}); }
async function getLoginQR(params) {
  if (state.msgService) return loginQRStatus();
  if (state.qrRequest) return state.qrRequest;
  if (!state.loginConnected) throw new Error('QQNT login service is not connected yet');
  if (!params.refresh && state.loginQR && Date.parse(state.loginQR.expires_at) > Date.now()) return loginQRStatus();
  if (typeof state.loginService?.getQRCodePicture !== 'function') throw new Error('QQNT login service does not expose getQRCodePicture');
  state.loginQR = null;
  publishLoginQR();
  state.qrRequest = (async () => {
    const picture = new Promise((resolve,reject) => { state.qrResolve = resolve; state.qrReject = reject; });
    let timer;
    const timeout = new Promise((_resolve,reject) => { timer = setTimeout(() => reject(new Error('QQNT QR picture callback timeout')),5000); });
    const request = Promise.resolve().then(() => state.loginService.getQRCodePicture()).then(result => {
      if (result?.pngBase64QrcodeData) recordLoginQR(result);
      else if (result && Object.hasOwn(result,'result') && String(result.result) !== '0') throw new Error('QQNT QR request failed');
    });
    try {
      await Promise.race([Promise.all([request,picture]),timeout]);
      return loginQRStatus();
    }
    finally { clearTimeout(timer); state.qrResolve = null; state.qrReject = null; }
  })().finally(() => { state.qrRequest = null; });
  return state.qrRequest;
}

function attachSession() {
  attachLoginListener();
  if (process.env.CINLAN_QQNT_HEADLESS === 'true' && !state.loginConfirmed) return;
  if (!state.self.uin) {
    return;
  }
  if (state.msgService) {
    void resolveSelfUID();
    attachAVListener();
    attachFriendListener();
    publishStatus('ready');
    return;
  }
  try {
    const sessionFactory = state.wrapper?.NodeIQQNTWrapperSession;
    if (!sessionFactory) throw new Error('QQNT wrapper does not expose session factory');
    const session = sessionFactory.getNTWrapperSession('nt_1');
    const msgService = session?.getMsgService?.();
    if (!msgService) throw new Error('QQNT message session is unavailable');
    const listener = listenerProxy({
      onRecvMsg: (messages) => {
        if (!Array.isArray(messages)) {
          return;
        }
        for (const message of messages) {
          enqueueNativeMessage(message);
        }
      },
      onAddSendMsg: (message) => {
        observeSendUpdates([message]);
        if (message?.senderUin) {
          updateSelf({
            uin: String(message.senderUin),
            uid: String(message.senderUid || ''),
            nick: String(message.sendNickName || ''),
          });
        }
      },
      onMsgInfoListUpdate: (messages) => {
        observeSendUpdates(messages);
      },
      onRichMediaDownloadComplete: (...args) => {
        observeRichMediaDownload(args);
      },
    });
    state.msgListenerID = msgService.addKernelMsgListener(listener);
    state.session = session;
    state.msgService = msgService;
    attachAVListener(session);
    attachFriendListener(session);
    clearRuntimeError('session_attach');
    void resolveSelfUID();
    publishStatus(state.self.uin ? 'ready' : 'waiting_login');
  } catch (error) {
    state.session = null;
    state.msgService = null;
    reportRuntimeError('session_attach', error);
  }
}

function attachAVListener(session = state.session) {
  if (state.avService || !session || typeof session.getAVSDKService !== 'function') {
    return;
  }
  try {
    const service = session.getAVSDKService();
    if (!service || typeof service.addKernelAVSDKListener !== 'function') {
      throw new Error('QQNT AVSDK service does not expose addKernelAVSDKListener');
    }
    const handlers = {};
    for (const callback of AVSDK_CALLBACK_NAMES) {
      handlers[callback] = (...args) => recordAVEvent(callback, args);
    }
    const listenerID = service.addKernelAVSDKListener(listenerProxy(handlers));
    state.avService = service;
    state.avListenerID = listenerID;
    state.avMethods = availableAVSDKMethods(service);
    clearRuntimeError('avsdk_attach');
    publishStatus();
  } catch (error) {
    state.avService = null;
    state.avListenerID = null;
    state.avMethods = [];
    reportRuntimeError('avsdk_attach', error);
  }
}

function availableAVSDKMethods(service) {
  return AVSDK_METHOD_NAMES.filter((name) => {
    try {
      return typeof service?.[name] === 'function';
    } catch {
      return false;
    }
  });
}

function attachFriendListener(session = state.session) {
  if (state.buddyService || !session) return;
  try {
    const service = session.getBuddyService?.();
    if (!service || typeof service.addKernelBuddyListener !== 'function' ||
        typeof service.approvalFriendRequest !== 'function') {
      throw new Error('QQNT buddy service is missing listener/approval bindings');
    }
    state.buddyListenerID = service.addKernelBuddyListener(listenerProxy({
      onBuddyReqChange: (info) => enqueueFriendRequests(info),
    }));
    state.buddyService = service;
    if (typeof service.getBuddyReq === 'function') {
      Promise.resolve(service.getBuddyReq()).then(info => {
        clearRuntimeError('friend_discovery');
        if (info?.buddyReqs) enqueueFriendRequests(info);
      }).catch(error => reportRuntimeError('friend_discovery', error));
    }
    clearRuntimeError('friend_attach');
    publishStatus();
  } catch (error) {
    reportRuntimeError('friend_attach', error);
  }
}

function enqueueFriendRequests(info) {
  if (state.friendQueued >= 32) {
    reportRuntimeError('friend_event', new Error('friend event queue full'));
    return;
  }
  state.friendQueued++;
  state.friendQueue = state.friendQueue.then(() => handleFriendRequests(info))
    .then(() => clearRuntimeError('friend_event'))
    .catch(error => reportRuntimeError('friend_event', error))
    .finally(() => { state.friendQueued--; });
}

async function handleFriendRequests(info) {
  if (!Array.isArray(info?.buddyReqs)) throw new Error('unknown QQNT buddy request callback shape');
  const generation = state.sessionGeneration;
  for (const request of info.buddyReqs) {
    // Outgoing, decided and doubt/spam requests must never be auto-approved.
    if (request.isInitiator !== false || request.isDecide !== false || request.isDoubt === true) continue;
    if (stringValue(request.reqTime).length > 20 || stringValue(request.friendUid).length > 128) throw new Error('oversized friend request identity');
    const uid = stringValue(request.friendUid);
    const reqTime = stringValue(request.reqTime);
    if (!uid || !/^[0-9]+$/.test(reqTime)) throw new Error('invalid QQNT friend request identity');
    const key = `${uid}:${reqTime}`;
    if (state.friendRequests.has(key)) continue;
    const profile = state.session?.getProfileService?.();
    const mapping = await profile?.getUinByUid?.('cinlan-qq-bot', [uid]);
    if (generation !== state.sessionGeneration) throw new Error('friend session changed during UID lookup');
    const uin = stringValue(mapping instanceof Map ? mapping.get(uid) : mapping?.[uid]);
    if (!/^[1-9][0-9]{0,19}$/.test(uin)) throw new Error('cannot resolve friend request UID to UIN');
    const flag = crypto.randomBytes(24).toString('hex');
    const entry = { uid, reqTime, flag, approved: false, at: Date.now() };
    state.friendRequests.set(key, entry);
    while (state.friendRequests.size > 512) state.friendRequests.delete(state.friendRequests.keys().next().value);
    if (!sendEnvelope({ type: 'friend_request', payload: {
      uin, uid, flag, comment: stringValue(request.extWords).slice(0, 1024),
      nickname: stringValue(request.friendNick).slice(0, 128),
    } })) {
      state.friendRequests.delete(key);
      throw new Error('friend request IPC delivery failed');
    }
  }
}

function checkOperateResult(result, operation) {
  if (!result || !Object.hasOwn(result, 'result') || String(result.result) !== '0') {
    throw new Error(`${operation} failed: ${stringValue(result?.errMsg || result?.result) || 'unknown response'}`);
  }
}

async function approveFriend(params) {
  if (typeof params.approve !== 'boolean' || typeof params.flag !== 'string' ||
      typeof params.remark !== 'string' || params.remark.length > 128) {
    throw new Error('approve_friend requires flag, boolean approve and remark (max 128 chars)');
  }
  if (!state.buddyService) throw new Error('QQNT buddy service is not ready');
  const request = [...state.friendRequests.values()].find(r => r.flag === params.flag);
  if (!request || request.approved || Date.now() - request.at > 24 * 3600 * 1000) {
    throw new Error('approve_friend flag is unknown, expired or already decided');
  }
  if (request.pending) throw new Error('approve_friend is already pending');
  request.pending = true;
  try {
    const result = await state.buddyService.approvalFriendRequest({
      friendUid: request.uid, accept: params.approve, refuseMsg: '', reqTime: request.reqTime,
    });
    checkOperateResult(result, 'QQNT friend approval');
    request.approved = true;
    if (params.approve && params.remark) {
      const remarkResult = await state.buddyService.setBuddyRemark({ uid: request.uid, remark: params.remark, signInfo: '' });
      checkOperateResult(remarkResult, 'QQNT friend remark');
    }
    clearRuntimeError('action_approve_friend');
    return { approved: params.approve };
  } finally { request.pending = false; }
}

function recordAVEvent(callback, args) {
  if (!AVSDK_CALLBACK_NAMES.includes(callback)) {
    return;
  }
  const values = Array.isArray(args) ? args : [];
  const actionCodeCandidate = values.find(
    (value) => typeof value === 'number' && Number.isSafeInteger(value),
  );
  const event = {
    sequence: ++state.avEventSequence,
    callback,
    received_at: new Date().toISOString(),
    action_code_candidate: actionCodeCandidate ?? null,
    argument_count: values.length,
    arguments: values
      .slice(0, MAX_AV_ARGUMENTS)
      .map((value) => {
        try {
          return summarizeAVArgument(value);
        } catch {
          return { type: 'unavailable' };
        }
      }),
    arguments_truncated: values.length > MAX_AV_ARGUMENTS,
  };
  state.recentAVEvents.push(event);
  if (state.recentAVEvents.length > MAX_RECENT_AV_EVENTS) {
    state.recentAVEvents.splice(
      0,
      state.recentAVEvents.length - MAX_RECENT_AV_EVENTS,
    );
  }
  sendEnvelope({ type: 'av_event', payload: event });
}

function summarizeAVArgument(value) {
  const bytes = byteView(value);
  if (bytes) {
    const summary = {
      type: 'buffer',
      byte_length: bytes.byteLength,
      oversized: bytes.byteLength > MAX_AV_BUFFER_BYTES,
    };
    if (!summary.oversized) {
      summary.sha256 = crypto
        .createHash('sha256')
        .update(bytes)
        .digest('hex');
    }
    return summary;
  }
  if (value === null) {
    return { type: 'null' };
  }
  if (value === undefined) {
    return { type: 'undefined' };
  }
  if (typeof value === 'number') {
    return Number.isFinite(value)
      ? { type: 'number', number_value: value }
      : { type: 'number', number_special: String(value) };
  }
  if (typeof value === 'bigint') {
    return { type: 'bigint', integer_value: value.toString() };
  }
  if (typeof value === 'boolean') {
    return { type: 'boolean', boolean_value: value };
  }
  if (typeof value === 'string') {
    const byteLength = Buffer.byteLength(value, 'utf8');
    const summary = {
      type: 'string',
      byte_length: byteLength,
      oversized: byteLength > MAX_AV_BUFFER_BYTES,
    };
    if (!summary.oversized) {
      summary.sha256 = crypto
        .createHash('sha256')
        .update(value, 'utf8')
        .digest('hex');
    }
    return summary;
  }
  if (typeof value === 'object') {
    let keys = [];
    let keysTruncated = false;
    try {
      const sourceKeys = Object.keys(value);
      keysTruncated = sourceKeys.length > MAX_AV_OBJECT_KEYS;
      keys = sourceKeys.slice(0, MAX_AV_OBJECT_KEYS)
        .map((key) => String(key).slice(0, 64));
    } catch {
      // Native proxies may reject enumeration; the object value is never read.
    }
    return {
      type: 'object',
      object_type: safeObjectType(value),
      keys,
      keys_truncated: keysTruncated,
    };
  }
  return { type: typeof value };
}

function byteView(value) {
  if (Buffer.isBuffer(value)) {
    return value;
  }
  if (ArrayBuffer.isView(value)) {
    return Buffer.from(value.buffer, value.byteOffset, value.byteLength);
  }
  if (value instanceof ArrayBuffer) {
    return Buffer.from(value);
  }
  return null;
}

function safeObjectType(value) {
  try {
    const name = value?.constructor?.name;
    return typeof name === 'string' ? name.slice(0, 64) : 'Object';
  } catch {
    return 'Object';
  }
}

function inspectAVSDK() {
  return {
    available: Boolean(state.avService),
    listener_attached: state.avListenerID !== null,
    listener_id: stringValue(state.avListenerID),
    methods: [...state.avMethods],
    recent_events: state.recentAVEvents.map((event) => ({
      ...event,
      arguments: event.arguments.map((argument) => ({
        ...argument,
        keys: Array.isArray(argument.keys) ? [...argument.keys] : undefined,
      })),
    })),
  };
}

function listenerProxy(handlers) {
  const noop = () => undefined;
  return new Proxy(handlers, {
    get(target, property) {
      if (property in target) {
        return target[property];
      }
      return typeof property === 'string' ? noop : target[property];
    },
  });
}

function findAndUpdateSelf(value) {
  if (!value || typeof value !== 'object') {
    return;
  }
  if (!Array.isArray(value) && (value.uin || value.uid)) {
    updateSelf(value);
    return;
  }
  for (const child of Object.values(value)) {
    if (child && typeof child === 'object') {
      findAndUpdateSelf(child);
    }
  }
}

function updateSelf(info) {
  const observedUin = stringValue(info?.uin || info?.account);
  const accountChanged = Boolean(
    observedUin && observedUin !== state.self.uin,
  );
  const next = {
    uin: observedUin || state.self.uin,
    uid: stringValue(info?.uid || (accountChanged ? '' : state.self.uid)),
    nick: stringValue(
      info?.nickName || info?.nick || (accountChanged ? '' : state.self.nick),
    ),
  };
  if (accountChanged) {
    state.selfUIDLookupUin = '';
    if (state.msgService) resetSession();
  }
  const changed =
    next.uin !== state.self.uin ||
    next.uid !== state.self.uid ||
    next.nick !== state.self.nick;
  state.self = next;
  if (changed) {
    publishStatus(state.msgService && state.self.uin ? 'ready' : 'waiting_login');
  }
  if (state.self.uin && !state.self.uid) {
    void resolveSelfUID();
  }
}

async function resolveSelfUID() {
  const uin = state.self.uin;
  if (!uin || state.self.uid || state.selfUIDLookupUin === uin || !state.session) {
    return;
  }
  state.selfUIDLookupUin = uin;
  try {
    const profile = state.session.getProfileService?.();
    const mapping = await profile?.getUidByUin?.('cinlan-qq-bot', [uin]);
    const uid = mapping instanceof Map ? mapping.get(uin) : mapping?.[uin];
    if (!uid) {
      throw new Error(`cannot resolve current QQ UIN ${uin} to UID`);
    }
    if (state.self.uin === uin) {
      updateSelf({ uin, uid: String(uid) });
    }
    clearRuntimeError('self_uid_resolution');
  } catch (error) {
    reportRuntimeError('self_uid_resolution', error);
  } finally {
    if (state.selfUIDLookupUin === uin) {
      state.selfUIDLookupUin = '';
    }
  }
}

function enqueueNativeMessage(raw) {
  state.messageQueue = state.messageQueue
    .then(() => handleNativeMessage(raw))
    .then(() => clearRuntimeError('message_event'))
    .catch((error) => reportRuntimeError('message_event', error));
}

async function handleNativeMessage(raw) {
  if (!raw || (raw.chatType !== CHAT_GROUP && raw.chatType !== CHAT_PRIVATE)) {
    return;
  }
  const messageTime = numberValue(raw.msgTime);
  if (messageTime > 0 && messageTime < state.bootTime) {
    return;
  }
  const messageID = stringValue(raw.msgId);
  const senderUin = stringValue(raw.senderUin);
  const chatID = raw.chatType === CHAT_GROUP
    ? stringValue(raw.peerUin || raw.peerUid)
    : senderUin;
  if (!messageID || !senderUin || !chatID || !state.self.uin) {
    return;
  }

  raw = await prepareInboundImages(raw);
  raw = await transcribePttMessage(raw);
  rememberMessage(raw);
  const chain = convertElements(raw.elements);
  sendEnvelope({
    type: 'event',
    payload: {
      id: `${state.self.uin}:${messageID}`,
      platform: 'qq-native',
      post_type: 'message',
      message_type: raw.chatType === CHAT_GROUP ? 'group' : 'private',
      sub_type: 'normal',
      message_id: messageID,
      self_id: state.self.uin,
      user_id: senderUin,
      chat_id: chatID,
      sender_name: stringValue(
        raw.sendMemberName || raw.sendRemarkName || raw.sendNickName,
      ),
      sender_role: '',
      chain,
      raw_message: chainToText(chain),
      metadata: {
        time: messageTime,
        native_chat_type: raw.chatType,
        sender_uid: stringValue(raw.senderUid),
        peer_uid: stringValue(raw.peerUid),
        peer_name: stringValue(raw.peerName),
      },
    },
  });
}

async function prepareInboundImages(raw) {
  let current = raw;
  let pictures = pictureElements(current);
  if (pictures.length === 0 || pictures.every(hasReadableImageReference)) {
    return current;
  }

  const stored = await getStoredMessage(current);
  if (stored) {
    current = stored;
    pictures = pictureElements(current);
  }

  for (const element of pictures) {
    if (hasReadableImageReference(element)) {
      continue;
    }
    try {
      const downloaded = await downloadInboundImage(current, element);
      element.picElement.filePath = downloaded;
      element.picElement.sourcePath = downloaded;
      clearRuntimeError('image_download');
    } catch (error) {
      reportRuntimeError('image_download', error);
    }
  }
  return current;
}

function pictureElements(raw) {
  return (Array.isArray(raw?.elements) ? raw.elements : [])
    .filter((element) =>
      numberValue(element?.elementType) === ELEMENT_PIC &&
      element?.picElement
    );
}

function hasReadableImageReference(element) {
  const picture = element?.picElement;
  const remote = stringValue(picture?.originImageUrl).trim().toLowerCase();
  if (remote.startsWith('https://') || remote.startsWith('http://')) {
    return true;
  }
  for (const candidate of [picture?.sourcePath, picture?.filePath]) {
    const localPath = stringValue(candidate).trim();
    if (!localPath || !path.isAbsolute(localPath)) {
      continue;
    }
    try {
      if (fs.statSync(localPath).isFile()) {
        return true;
      }
    } catch {
      // QQ may report the sender's path; download it into this account's cache.
    }
  }
  return false;
}

async function getStoredMessage(raw) {
  if (typeof state.msgService?.getMsgsByMsgId !== 'function') {
    return null;
  }
  const messageID = stringValue(raw?.msgId);
  if (!messageID) {
    return null;
  }
  try {
    const response = await state.msgService.getMsgsByMsgId({
      chatType: raw.chatType,
      peerUid: stringValue(raw.peerUid),
      guildId: stringValue(raw.guildId),
    }, [messageID]);
    return (Array.isArray(response?.msgList) ? response.msgList : [])
      .find((message) => stringValue(message?.msgId) === messageID) || null;
  } catch {
    return null;
  }
}

async function downloadInboundImage(raw, element) {
  if (typeof state.msgService?.downloadRichMedia !== 'function') {
    throw new Error('QQNT message service does not expose downloadRichMedia');
  }
  const messageID = stringValue(raw?.msgId);
  const elementID = stringValue(element?.elementId);
  const peerUID = stringValue(raw?.peerUid);
  if (!messageID || !elementID || !peerUID) {
    throw new Error('QQNT image is missing download identifiers');
  }

  const confirmation = createMediaDownloadConfirmation(messageID, elementID);
  try {
    const result = state.msgService.downloadRichMedia({
      fileModelId: '0',
      downSourceType: 0,
      downloadSourceType: 0,
      triggerType: 1,
      msgId: messageID,
      chatType: raw.chatType,
      peerUid: peerUID,
      elementId: elementID,
      thumbSize: 0,
      downloadType: 1,
      filePath: '',
    });
    Promise.resolve(result).catch((error) => confirmation.fail(error));
  } catch (error) {
    confirmation.cancel();
    throw error;
  }
  return confirmation.promise;
}

function mediaDownloadKey(messageID, elementID) {
  return `${messageID}\u0000${elementID}`;
}

function createMediaDownloadConfirmation(messageID, elementID) {
  const key = mediaDownloadKey(messageID, elementID);
  let resolvePromise;
  let rejectPromise;
  const promise = new Promise((resolve, reject) => {
    resolvePromise = resolve;
    rejectPromise = reject;
  });
  const pending = {
    timer: setTimeout(() => {
      if (state.pendingMediaDownloads.get(key) !== pending) {
        return;
      }
      state.pendingMediaDownloads.delete(key);
      rejectPromise(new Error('timed out waiting for QQNT image download'));
    }, RICH_MEDIA_DOWNLOAD_TIMEOUT_MS),
    resolve(filePath) {
      if (state.pendingMediaDownloads.get(key) !== pending) {
        return;
      }
      state.pendingMediaDownloads.delete(key);
      clearTimeout(pending.timer);
      resolvePromise(filePath);
    },
    reject(error) {
      if (state.pendingMediaDownloads.get(key) !== pending) {
        return;
      }
      state.pendingMediaDownloads.delete(key);
      clearTimeout(pending.timer);
      rejectPromise(error);
    },
  };
  state.pendingMediaDownloads.set(key, pending);
  return {
    promise,
    fail(error) {
      pending.reject(error instanceof Error ? error : new Error(String(error)));
    },
    cancel() {
      if (state.pendingMediaDownloads.get(key) === pending) {
        state.pendingMediaDownloads.delete(key);
        clearTimeout(pending.timer);
      }
    },
  };
}

function observeRichMediaDownload(args) {
  const values = Array.isArray(args) ? args : [args];
  const event = values.find((value) =>
    value && typeof value === 'object' &&
    stringValue(value.msgId) &&
    stringValue(value.msgElementId || value.elementId)
  );
  if (!event) {
    return;
  }
  const key = mediaDownloadKey(
    stringValue(event.msgId),
    stringValue(event.msgElementId || event.elementId),
  );
  const pending = state.pendingMediaDownloads.get(key);
  if (!pending) {
    return;
  }
  const filePath = stringValue(
    event.filePath || event.downloadedFilePath || event.sourcePath,
  );
  try {
    if (!filePath || !path.isAbsolute(filePath) ||
        !fs.statSync(filePath).isFile()) {
      pending.reject(new Error('QQNT image download returned no readable file'));
      return;
    }
  } catch {
    pending.reject(new Error('QQNT image download returned no readable file'));
    return;
  }
  pending.resolve(filePath);
}

async function transcribePttMessage(raw) {
  const pttElements = (Array.isArray(raw?.elements) ? raw.elements : [])
    .filter((element) =>
      numberValue(element?.elementType) === ELEMENT_PTT &&
      element?.pttElement &&
      !stringValue(element.pttElement.text).trim()
    );
  if (pttElements.length === 0) {
    return raw;
  }
  if (typeof state.msgService?.translatePtt2Text !== 'function') {
    throw new Error('QQNT message service does not expose translatePtt2Text');
  }
  if (typeof state.msgService.getMsgsByMsgId !== 'function') {
    throw new Error('QQNT message service does not expose getMsgsByMsgId');
  }

  const messageID = stringValue(raw.msgId);
  const peer = {
    chatType: raw.chatType,
    peerUid: stringValue(raw.peerUid),
    guildId: stringValue(raw.guildId),
  };
  const storedResponse = await state.msgService.getMsgsByMsgId(peer, [messageID]);
  const stored = (Array.isArray(storedResponse?.msgList)
    ? storedResponse.msgList
    : [])
    .find((message) => stringValue(message?.msgId) === messageID);
  if (!stored) {
    throw new Error('QQNT PTT message is not available from getMsgsByMsgId');
  }
  const storedPtt = (Array.isArray(stored.elements) ? stored.elements : [])
    .filter((element) => numberValue(element?.elementType) === ELEMENT_PTT);
  if (storedPtt.length < pttElements.length) {
    throw new Error('QQNT stored message does not contain the expected PTT elements');
  }
  if (storedPtt.every((element) =>
    stringValue(element?.pttElement?.text).trim()
  )) {
    return stored;
  }

  for (const element of storedPtt) {
    await state.msgService.translatePtt2Text(messageID, peer, element);
  }
  if (storedPtt.every((element) =>
    stringValue(element?.pttElement?.text).trim()
  )) {
    return stored;
  }

  for (let attempt = 0; attempt < PTT_TRANSCRIPTION_POLL_ATTEMPTS; attempt += 1) {
    const response = await state.msgService.getMsgsByMsgId(peer, [messageID]);
    const refreshed = (Array.isArray(response?.msgList) ? response.msgList : [])
      .find((message) => stringValue(message?.msgId) === messageID);
    const refreshedPtt = (Array.isArray(refreshed?.elements)
      ? refreshed.elements
      : [])
      .filter((element) => numberValue(element?.elementType) === ELEMENT_PTT);
    if (
      refreshed &&
      refreshedPtt.length >= storedPtt.length &&
      refreshedPtt.every((element) =>
        stringValue(element?.pttElement?.text).trim()
      )
    ) {
      return refreshed;
    }
    if (attempt + 1 < PTT_TRANSCRIPTION_POLL_ATTEMPTS) {
      await sleep(PTT_TRANSCRIPTION_POLL_DELAY_MS);
    }
  }
  throw new Error('QQNT PTT transcription completed without text');
}

function rememberMessage(raw) {
  const id = stringValue(raw.msgId);
  if (!id) {
    return;
  }
  state.recentMessages.delete(id);
  state.recentMessages.set(id, {
    msgSeq: raw.msgSeq,
    msgId: raw.msgId,
    senderUin: raw.senderUin,
    senderUid: raw.senderUid,
    clientSeq: raw.clientSeq,
    msgTime: raw.msgTime,
    chatType: raw.chatType,
    peerUid: raw.peerUid,
    guildId: raw.guildId,
  });
  if (state.recentMessages.size <= 1024) {
    return;
  }
  const oldest = state.recentMessages.keys().next().value;
  state.recentMessages.delete(oldest);
}

function convertElements(elements) {
  if (!Array.isArray(elements)) {
    return [];
  }
  const chain = [];
  for (const element of elements) {
    if (!element || typeof element !== 'object') {
      continue;
    }
    if (element.textElement) {
      const text = element.textElement;
      const atType = numberValue(text.atType);
      let target = stringValue(
        text.atUid || text.atNtUid || text.atUin || text.atNtUin,
      );
      if (target === '0') {
        target = '';
      }
      if (atType === AT_UNKNOWN && !target) {
        if (stringValue(text.content)) {
          chain.push({ type: 'text', data: { text: stringValue(text.content) } });
        }
      } else {
        if (
          state.self.uid &&
          (target === state.self.uid ||
            stringValue(text.atNtUid) === state.self.uid)
        ) {
          target = state.self.uin;
        }
        if (atType === AT_ALL) {
          target = 'all';
        }
        chain.push({
          type: 'at',
          data: { qq: target, name: stringValue(text.content) },
        });
      }
      continue;
    }
    const converted = convertRichElement(element);
    if (converted) {
      chain.push(converted);
    }
  }
  return chain;
}

function convertRichElement(element) {
  switch (numberValue(element.elementType)) {
    case ELEMENT_PIC:
      return { type: 'image', data: compactData(element.picElement, ['filePath', 'sourcePath', 'originImageUrl', 'md5HexStr']) };
    case ELEMENT_FILE:
    case ELEMENT_ONLINE_FILE:
      return { type: 'file', data: compactData(element.fileElement, ['fileName', 'filePath', 'fileSize', 'fileUuid']) };
    case ELEMENT_PTT:
      return { type: 'record', data: compactData(element.pttElement, ['fileName', 'filePath', 'duration', 'text']) };
    case ELEMENT_VIDEO:
      return { type: 'video', data: compactData(element.videoElement, ['fileName', 'filePath', 'duration']) };
    case ELEMENT_FACE:
      return { type: 'face', data: compactData(element.faceElement, ['faceIndex', 'faceText', 'packId', 'stickerId']) };
    case ELEMENT_REPLY:
      return {
        type: 'reply',
        data: {
          id: stringValue(
            element.replyElement?.replayMsgId ||
            element.replyElement?.sourceMsgIdInRecords,
          ),
        },
      };
    case ELEMENT_ARK:
      return { type: 'json', data: { data: stringValue(element.arkElement?.bytesData || element.arkElement?.json) } };
    case ELEMENT_MARKET_FACE:
      return { type: 'mface', data: compactData(element.marketFaceElement, ['emojiId', 'emojiPackageId', 'faceName']) };
    case ELEMENT_MARKDOWN:
      return { type: 'markdown', data: { content: stringValue(element.markdownElement?.content) } };
    case ELEMENT_MULTI_FORWARD: {
      const text = extractMultiForwardText(element);
      if (text) {
        return {
          type: 'text',
          data: { text: `[聊天记录]\n${text}` },
        };
      }
      return {
        type: 'native_16',
        data: { element_id: stringValue(element.elementId) },
      };
    }
    default:
      return {
        type: `native_${numberValue(element.elementType)}`,
        data: { element_id: stringValue(element.elementId) },
      };
  }
}

async function handleAction(payload) {
  const name = stringValue(payload.name);
  const params = payload.params && typeof payload.params === 'object'
    ? payload.params
    : {};
  switch (name) {
    case 'runtime_status':
      return runtimeStatus();
    case 'inspect_avsdk':
      return inspectAVSDK();
    case 'approve_friend':
      return approveFriend(params);
    case 'get_login_qr':
      return getLoginQR(params);
    case 'get_login_list':
      return getLoginList();
    case 'quick_login':
      return quickLogin(params);
    case 'get_group_member_info':
      return getGroupMemberInfo(params);
    case 'send_message':
      return sendMessage(params);
    default:
      throw new Error(`unsupported native action ${JSON.stringify(name)}`);
  }
}

async function getLoginList() {
  if (!state.loginService) {
    throw new Error('QQNT login service is not ready');
  }
  return state.loginService.getLoginList();
}

async function quickLogin(params) {
  if (!state.loginService) {
    throw new Error('QQNT login service is not ready');
  }
  const uin = stringValue(params.uin);
  if (!uin) {
    throw new Error('quick_login uin is required');
  }
  if (state.msgService && state.self.uin === uin) {
    return {
      result: '0',
      requested_uin: uin,
      already_logged_in: true,
    };
  }
  const loginList = await state.loginService.getLoginList();
  const account = Array.isArray(loginList?.LocalLoginInfoList)
    ? loginList.LocalLoginInfoList.find((entry) => stringValue(entry?.uin) === uin)
    : undefined;
  if (!account) {
    throw new Error(`QQ ${uin} is not present in the local login list`);
  }
  if (!account.isQuickLogin) {
    throw new Error(`QQ ${uin} is not available for quick login`);
  }
  const result = await state.loginService.quickLoginWithUin(uin);
  const errorMessage = stringValue(result?.loginErrorInfo?.errMsg);
  if (stringValue(result?.result) !== '0' || errorMessage) {
    throw new Error(errorMessage || `QQNT quick login failed with result ${stringValue(result?.result)}`);
  }
  state.loginConfirmed = true;
  updateSelf(account);
  attachSession();
  return {
    result: stringValue(result?.result),
    requested_uin: uin,
  };
}

async function getGroupMemberInfo(params) {
  if (!state.session) {
    throw new Error('QQNT session is not ready');
  }
  const groupID = stringValue(params.group_id).trim();
  const userID = stringValue(params.user_id).trim();
  if (!/^[1-9]\d{0,19}$/.test(groupID)) {
    throw new Error('get_group_member_info group_id is invalid');
  }
  if (!/^[1-9]\d{0,19}$/.test(userID)) {
    throw new Error('get_group_member_info user_id is invalid');
  }
  const profile = state.session.getProfileService?.();
  const mapping = await profile?.getUidByUin?.('cinlan-qq-bot', [userID]);
  const uid = mapping instanceof Map ? mapping.get(userID) : mapping?.[userID];
  if (!uid) {
    throw new Error(`cannot resolve QQ ${userID} to UID for group membership`);
  }
  const groupService = state.session.getGroupService?.();
  if (!groupService || typeof groupService.getAllMemberList !== 'function') {
    throw new Error('QQNT group service does not expose getAllMemberList');
  }

  let response = await groupService.getAllMemberList(groupID, false);
  validateGroupMemberResponse(groupID, response);
  let member = groupMemberExists(response?.result, String(uid));
  if (!member) {
    response = await groupService.getAllMemberList(groupID, true);
    validateGroupMemberResponse(groupID, response);
    member = groupMemberExists(response?.result, String(uid));
  }
  return {
    group_id: groupID,
    user_id: userID,
    member,
  };
}

function validateGroupMemberResponse(groupID, response) {
  const errorCode = numberValue(response?.errCode);
  if (errorCode !== 0) {
    const detail = stringValue(response?.errMsg).trim();
    throw new Error(
      `QQNT group member query for ${groupID} failed with ${errorCode}` +
      (detail ? `: ${detail}` : ''),
    );
  }
  if (!response?.result || !response.result.infos) {
    throw new Error(`QQNT group member query for ${groupID} returned no member map`);
  }
}

function extractMultiForwardText(element) {
  const source =
    element?.multiForwardMsgElement ||
    element?.multiForwardElement ||
    element?.forwardMsgElement ||
    element?.chatRecordElement ||
    element;
  const parts = [];
  collectForwardText(source, parts, new Set(), 0);
  return normalizeForwardText(parts.join('\n'));
}

function collectForwardText(value, parts, seen, depth) {
  if (depth > 6 || value === null || value === undefined) {
    return;
  }
  if (typeof value === 'string') {
    const text = normalizeForwardText(value);
    if (text) {
      parts.push(text);
    }
    return;
  }
  if (typeof value !== 'object') {
    return;
  }
  if (seen.has(value)) {
    return;
  }
  seen.add(value);
  if (Array.isArray(value)) {
    for (const item of value) {
      collectForwardText(item, parts, seen, depth + 1);
    }
    return;
  }

  const preferredKeys = [
    'xmlContent',
    'xml_content',
    'text',
    'content',
    'title',
    'summary',
    'preview',
    'description',
    'messages',
    'messageList',
    'message_list',
    'nodes',
    'items',
    'records',
    'forwardMsg',
    'forward_msg',
  ];
  for (const key of preferredKeys) {
    if (Object.prototype.hasOwnProperty.call(value, key)) {
      collectForwardText(value[key], parts, seen, depth + 1);
    }
  }
}

function normalizeForwardText(value) {
  let text = stringValue(value).trim();
  if (!text) {
    return '';
  }
  if (/<[a-z][^>]*>/i.test(text)) {
    text = text
      .replace(/<!\[CDATA\[([\s\S]*?)\]\]>/gi, '$1')
      .replace(/<br\s*\/?>/gi, '\n')
      .replace(/<\/(?:p|item|node|msg|title|desc)>/gi, '\n')
      .replace(/<[^>]+>/g, '');
  }
  text = decodeXmlEntities(text)
    .replace(/\r\n?/g, '\n')
    .replace(/[ \t]+\n/g, '\n')
    .replace(/\n{3,}/g, '\n\n')
    .trim();
  return text.slice(0, 6000);
}

function decodeXmlEntities(value) {
  return value
    .replace(/&amp;/gi, '&')
    .replace(/&lt;/gi, '<')
    .replace(/&gt;/gi, '>')
    .replace(/&quot;/gi, '"')
    .replace(/&apos;/gi, "'")
    .replace(/&#x([0-9a-f]+);/gi, (_match, hex) => {
      const codePoint = Number.parseInt(hex, 16);
      return Number.isFinite(codePoint) && codePoint >= 0 && codePoint <= 0x10ffff
        ? String.fromCodePoint(codePoint)
        : '';
    })
    .replace(/&#([0-9]+);/g, (_match, decimal) => {
      const codePoint = Number.parseInt(decimal, 10);
      return Number.isFinite(codePoint) && codePoint >= 0 && codePoint <= 0x10ffff
        ? String.fromCodePoint(codePoint)
        : '';
    });
}

function groupMemberExists(result, uid) {
  const infos = result?.infos;
  if (infos instanceof Map) {
    return infos.has(uid);
  }
  return Boolean(
    infos &&
    typeof infos === 'object' &&
    Object.prototype.hasOwnProperty.call(infos, uid),
  );
}

async function sendMessage(params) {
  if (!state.msgService || !state.session || !state.self.uin) {
    throw new Error('QQNT message service is not ready');
  }
  const chatTypeName = stringValue(params.chat_type);
  const chatID = stringValue(params.chat_id);
  if (!chatID) {
    throw new Error('send_message chat_id is required');
  }

  let chatType;
  let peerUID = chatID;
  if (chatTypeName === 'group') {
    chatType = CHAT_GROUP;
  } else if (chatTypeName === 'private') {
    chatType = CHAT_PRIVATE;
    peerUID = await resolvePrivateUID(chatID);
  } else {
    throw new Error(`unsupported chat_type ${JSON.stringify(chatTypeName)}`);
  }

  const peer = { chatType, peerUid: peerUID, guildId: '' };
  const components = Array.isArray(params.chain) ? params.chain : [];
  const fileComponents = components.filter((component) => component?.type === 'file');
  if (fileComponents.length > 0) {
    if (chatType !== CHAT_PRIVATE) {
      throw new Error('native file delivery currently requires a private chat');
    }
    if (fileComponents.length !== 1 || components.length !== 1 || params.quote || params.reply_to) {
      throw new Error('native file delivery cannot be mixed with text or reply components');
    }
    return sendPrivateFile(peer, fileComponents[0]);
  }

  const elements = [];
  if (params.quote && params.reply_to) {
    elements.push(buildReplyElement(stringValue(params.reply_to), peer));
  }
  for (const component of components) {
    if (component?.type === 'text') {
      const text = stringValue(component?.data?.text);
      if (text) {
        elements.push({
          elementType: ELEMENT_TEXT,
          elementId: '',
          textElement: {
            content: text,
            atType: AT_UNKNOWN,
            atUid: '',
            atTinyId: '',
            atNtUid: '',
          },
        });
      }
      continue;
    }
    if (component?.type === 'at') {
      elements.push(await buildAtElement(component, peer));
      continue;
    }
    if (component?.type === 'image') {
      elements.push(await buildImageElement(component));
      continue;
    }
    throw new Error(`native v1 cannot send component ${JSON.stringify(component?.type)}`);
  }
  if (elements.length === 0 || (elements.length === 1 && elements[0].elementType === ELEMENT_REPLY)) {
    throw new Error('send_message contains no sendable content');
  }

  const result = await state.msgService.sendMsg('0', peer, elements, new Map());
  const code = result?.result;
  if (code !== undefined && String(code) !== '0') {
    throw new Error(`QQNT sendMsg failed with result ${String(code)}`);
  }
  return {
    result: code === undefined ? 0 : code,
    message_id: stringValue(result?.msgId || result?.messageId),
  };
}

async function buildImageElement(component) {
  const source = resolveAllowedSendImage(stringValue(component?.data?.file));
  const data = fs.readFileSync(source);
  const image = inspectSendImage(data);
  const md5 = crypto.createHash('md5').update(data).digest('hex');
  const fileName = path.basename(source);
  if (typeof state.msgService?.getRichMediaFilePathForGuild !== 'function') {
    throw new Error('QQNT message service cannot prepare an image upload');
  }
  const mediaPath = stringValue(state.msgService.getRichMediaFilePathForGuild({
    md5HexStr: md5,
    fileName,
    elementType: ELEMENT_PIC,
    elementSubType: 0,
    thumbSize: 0,
    needCreate: true,
    downloadType: 1,
    file_uuid: '',
  }));
  if (!mediaPath || !path.isAbsolute(mediaPath)) {
    throw new Error('QQNT returned an invalid image cache path');
  }
  fs.mkdirSync(path.dirname(mediaPath), { recursive: true });
  if (path.resolve(mediaPath) !== path.resolve(source)) {
    fs.copyFileSync(source, mediaPath);
  }
  return {
    elementType: ELEMENT_PIC,
    elementId: '',
    picElement: {
      md5HexStr: md5,
      filePath: mediaPath,
      fileSize: String(data.length),
      picWidth: image.width,
      picHeight: image.height,
      fileName,
      sourcePath: mediaPath,
      original: true,
      picType: image.picType,
      picSubType: 0,
      fileUuid: '',
      fileSubId: '',
      thumbFileSize: 0,
      summary: stringValue(component?.data?.summary),
      thumbPath: new Map(),
    },
  };
}

function resolveAllowedSendImage(value) {
  if (!value || !path.isAbsolute(value)) {
    throw new Error('native image path must be absolute');
  }
  const roots = imageSendRoots();
  if (roots.length === 0) {
    throw new Error('native image sending is not enabled');
  }
  let resolved;
  try {
    resolved = fs.realpathSync(value);
  } catch {
    throw new Error('native image file does not exist');
  }
  const allowed = roots.some((root) => {
    let resolvedRoot;
    try {
      resolvedRoot = fs.realpathSync(root);
    } catch {
      return false;
    }
    const relative = path.relative(resolvedRoot, resolved);
    return relative !== '..' &&
      !relative.startsWith(`..${path.sep}`) &&
      !path.isAbsolute(relative);
  });
  if (!allowed) {
    throw new Error('native image path is outside the allowed roots');
  }
  const info = fs.statSync(resolved);
  const maximum = imageSendMaxBytes();
  if (!info.isFile() || info.size <= 0 || info.size > maximum) {
    throw new Error(`native image must be a regular file up to ${maximum} bytes`);
  }
  return resolved;
}

function inspectSendImage(data) {
  if (!Buffer.isBuffer(data) || data.length < 12) {
    throw new Error('native image is empty or truncated');
  }
  if (data.subarray(0, 8).equals(Buffer.from([
    0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
  ]))) {
    if (data.length < 24) {
      throw new Error('native PNG is truncated');
    }
    return validateImageDimensions(
      data.readUInt32BE(16),
      data.readUInt32BE(20),
      PIC_TYPE_PNG,
    );
  }
  if (data.subarray(0, 6).toString('ascii') === 'GIF87a' ||
    data.subarray(0, 6).toString('ascii') === 'GIF89a') {
    return validateImageDimensions(
      data.readUInt16LE(6),
      data.readUInt16LE(8),
      PIC_TYPE_GIF,
    );
  }
  if (data[0] === 0xff && data[1] === 0xd8) {
    let offset = 2;
    while (offset + 9 < data.length) {
      if (data[offset] !== 0xff) {
        offset += 1;
        continue;
      }
      const marker = data[offset + 1];
      if (marker === 0xd8 || marker === 0x01) {
        offset += 2;
        continue;
      }
      if (marker === 0xd9 || marker === 0xda) {
        break;
      }
      const length = data.readUInt16BE(offset + 2);
      if (length < 2 || offset + 2 + length > data.length) {
        break;
      }
      if ((marker >= 0xc0 && marker <= 0xc3) ||
        (marker >= 0xc5 && marker <= 0xc7) ||
        (marker >= 0xc9 && marker <= 0xcb) ||
        (marker >= 0xcd && marker <= 0xcf)) {
        return validateImageDimensions(
          data.readUInt16BE(offset + 7),
          data.readUInt16BE(offset + 5),
          PIC_TYPE_JPEG,
        );
      }
      offset += 2 + length;
    }
    throw new Error('native JPEG dimensions cannot be read');
  }
  throw new Error('native image type is not JPEG, PNG, or GIF');
}

function validateImageDimensions(width, height, picType) {
  if (!Number.isInteger(width) || !Number.isInteger(height) ||
    width < 1 || height < 1 || width > 4096 || height > 4096) {
    throw new Error('native image dimensions are invalid');
  }
  return { width, height, picType };
}

function imageSendRoots() {
  return stringValue(process.env.CINLAN_QQNT_SEND_IMAGE_ROOTS)
    .split(';')
    .map((value) => value.trim())
    .filter(Boolean);
}

function imageSendMaxBytes() {
  const raw = stringValue(process.env.CINLAN_QQNT_SEND_IMAGE_MAX_BYTES);
  if (!raw) {
    return DEFAULT_SEND_IMAGE_MAX_BYTES;
  }
  const parsed = Number(raw);
  if (!Number.isSafeInteger(parsed) || parsed < 1) {
    throw new Error('CINLAN_QQNT_SEND_IMAGE_MAX_BYTES must be a positive integer');
  }
  return parsed;
}

async function buildAtElement(component, peer) {
  if (peer.chatType !== CHAT_GROUP) {
    throw new Error('native at component requires a group chat');
  }
  const target = stringValue(component?.data?.qq);
  if (!target) {
    throw new Error('native at component target is empty');
  }
  if (target === 'all') {
    return {
      elementType: ELEMENT_TEXT,
      elementId: '',
      textElement: {
        content: '@全体成员',
        atType: AT_ALL,
        atUid: 'all',
        atTinyId: '',
        atNtUid: 'all',
      },
    };
  }
  const profile = state.session?.getProfileService?.();
  const mapping = await profile?.getUidByUin?.('cinlan-qq-bot', [target]);
  const uid = mapping instanceof Map ? mapping.get(target) : mapping?.[target];
  if (!uid) {
    throw new Error(`cannot resolve QQ ${target} to UID for mention`);
  }
  return {
    elementType: ELEMENT_TEXT,
    elementId: '',
    textElement: {
      content: `@${stringValue(component?.data?.name) || target}`,
      atType: AT_ONE,
      atUid: target,
      atTinyId: '',
      atNtUid: String(uid),
    },
  };
}

async function sendPrivateFile(peer, component) {
  const filePath = stringValue(component?.data?.file);
  if (!filePath || !path.isAbsolute(filePath)) {
    throw new Error('native file component requires an absolute local path');
  }
  let stat;
  try {
    stat = fs.statSync(filePath);
  } catch (error) {
    throw new Error(`native file path is not readable: ${error.message}`);
  }
  if (!stat.isFile()) {
    throw new Error('native file path is not a regular file');
  }
  const actualFileName = path.basename(
    stringValue(component?.data?.name) || filePath,
  );
  const fileElement = {
    elementType: ELEMENT_FILE,
    elementId: '',
    fileElement: {
      fileName: actualFileName,
      filePath,
      fileSize: String(stat.size),
    },
  };
  if (stat.size === 0) {
    throw new Error('native file component cannot send an empty file');
  }
  if (typeof state.msgService.generateMsgUniqueId !== 'function') {
    throw new Error('QQNT message service does not expose generateMsgUniqueId');
  }
  const msfService = state.session.getMSFService?.();
  if (!msfService || typeof msfService.getServerTime !== 'function') {
    throw new Error('QQNT session does not expose MSF server time');
  }
  const correlationID = stringValue(
    await state.msgService.generateMsgUniqueId(
      peer.chatType,
      msfService.getServerTime(),
    ),
  );
  if (!correlationID) {
    throw new Error('QQNT did not generate a file message correlation ID');
  }
  const sendPeer = { ...peer, guildId: correlationID };
  const confirmation = createSendConfirmation(correlationID);
  try {
    const sendResult = state.msgService.sendMsg(
      '0',
      sendPeer,
      [fileElement],
      new Map(),
    );
    Promise.resolve(sendResult).then((result) => {
      const code = result?.result;
      if (code !== undefined && String(code) !== '0') {
        confirmation.fail(
          new Error(`QQNT sendMsg failed with result ${String(code)}`),
        );
      }
    }, (error) => {
      confirmation.fail(error);
    });
  } catch (error) {
    confirmation.cancel();
    throw new Error(`QQNT file send failed: ${error.message}`);
  }

  let sent;
  try {
    sent = await confirmation.promise;
  } catch (error) {
    throw new Error(`QQNT file send failed: ${error.message || String(error)}`);
  }
  return {
    result: 0,
    message_id: stringValue(sent.msgId || sent.messageId),
  };
}

function createSendConfirmation(correlationID) {
  let resolvePromise;
  let rejectPromise;
  const promise = new Promise((resolve, reject) => {
    resolvePromise = resolve;
    rejectPromise = reject;
  });
  const pending = {
    timer: setTimeout(() => {
      if (state.pendingSends.get(correlationID) !== pending) {
        return;
      }
      state.pendingSends.delete(correlationID);
      rejectPromise(new Error('timed out waiting for QQNT send confirmation'));
    }, FILE_SEND_CONFIRM_TIMEOUT_MS),
    resolve(message) {
      if (state.pendingSends.get(correlationID) !== pending) {
        return;
      }
      state.pendingSends.delete(correlationID);
      clearTimeout(pending.timer);
      resolvePromise(message);
    },
    reject(error) {
      if (state.pendingSends.get(correlationID) !== pending) {
        return;
      }
      state.pendingSends.delete(correlationID);
      clearTimeout(pending.timer);
      rejectPromise(error);
    },
  };
  state.pendingSends.set(correlationID, pending);
  return {
    promise,
    fail(error) {
      pending.reject(error instanceof Error ? error : new Error(String(error)));
    },
    cancel() {
      if (state.pendingSends.get(correlationID) === pending) {
        state.pendingSends.delete(correlationID);
        clearTimeout(pending.timer);
      }
    },
  };
}

function observeSendUpdates(messages) {
  if (!Array.isArray(messages)) {
    return;
  }
  for (const message of messages) {
    const correlationID = stringValue(message?.guildId);
    const pending = state.pendingSends.get(correlationID);
    if (!pending) {
      continue;
    }
    const status = numberValue(message?.sendStatus);
    if (status === SEND_STATUS_SUCCESS) {
      pending.resolve(message);
    } else if (status === SEND_STATUS_FAILED) {
      pending.reject(new Error('QQNT reported a failed file message'));
    }
  }
}

function sleep(milliseconds) {
  return new Promise((resolve) => setTimeout(resolve, milliseconds));
}

function buildReplyElement(messageID, fallbackPeer) {
  const raw = state.recentMessages.get(messageID);
  if (!raw) {
    throw new Error(`reply source ${JSON.stringify(messageID)} is not in the runtime cache`);
  }
  return {
    elementType: ELEMENT_REPLY,
    elementId: '',
    replyElement: {
      replayMsgSeq: stringValue(raw.msgSeq),
      replayMsgId: stringValue(raw.msgId),
      senderUin: stringValue(raw.senderUin),
      senderUinStr: stringValue(raw.senderUin),
      senderUidStr: stringValue(raw.senderUid),
      replyMsgClientSeq: stringValue(raw.clientSeq),
      replyMsgTime: stringValue(raw.msgTime),
      _replyMsgPeer: {
        chatType: numberValue(raw.chatType) || fallbackPeer.chatType,
        peerUid: stringValue(raw.peerUid) || fallbackPeer.peerUid,
        guildId: stringValue(raw.guildId),
      },
    },
  };
}

async function resolvePrivateUID(uin) {
  if (!/^\d+$/.test(uin)) {
    return uin;
  }
  const profile = state.session?.getProfileService?.();
  const mapping = await profile?.getUidByUin?.('cinlan-qq-bot', [uin]);
  const resolved = mapping instanceof Map
    ? mapping.get(uin)
    : mapping?.[uin];
  if (!resolved) {
    throw new Error(`cannot resolve private QQ UIN ${uin} to UID`);
  }
  return String(resolved);
}

function publishStatus(forcedState, socket) {
  const target = socket || state.socket;
  const payload = {
    state: forcedState || runtimeState(),
    self_id: state.self.uin,
    self_uid: state.self.uid,
    nickname: state.self.nick,
    wrapper_loaded: Boolean(state.wrapper),
    session_attached: Boolean(state.msgService),
    avsdk_available: Boolean(state.avService),
    avsdk_listener_attached: state.avListenerID !== null,
    avsdk_methods: [...state.avMethods],
    friend_listener_attached: Boolean(state.buddyService),
    last_error: latestRuntimeError(),
  };
  const fingerprint = JSON.stringify(payload);
  if (
    state.statusSocket === target &&
    state.statusFingerprint === fingerprint
  ) {
    return false;
  }
  const sent = sendEnvelope({
    type: 'runtime_status',
    payload,
  }, target);
  if (sent) {
    state.statusSocket = target;
    state.statusFingerprint = fingerprint;
  }
  return sent;
}

function runtimeState() {
  if (!state.wrapper) {
    return 'booting';
  }
  if (!state.msgService || !state.self.uin) {
    return 'waiting_login';
  }
  return 'ready';
}

function runtimeStatus() {
  return {
    state: runtimeState(),
    self_id: state.self.uin,
    self_uid: state.self.uid,
    nickname: state.self.nick,
    wrapper_loaded: Boolean(state.wrapper),
    session_attached: Boolean(state.msgService),
    avsdk_available: Boolean(state.avService),
    avsdk_listener_attached: state.avListenerID !== null,
    avsdk_methods: [...state.avMethods],
    last_error: latestRuntimeError(),
  };
}

function sendEnvelope(envelope, socket = state.socket) {
  if (!socket || socket.connecting || socket.destroyed || !socket.writable) {
    return false;
  }
  const frame = JSON.stringify(
    { v: PROTOCOL_VERSION, ...envelope },
    (_key, value) => {
      if (typeof value === 'bigint') {
        return value.toString();
      }
      if (value instanceof Map) {
        return Object.fromEntries(value);
      }
      return value;
    },
  );
  if (Buffer.byteLength(frame, 'utf8') + 1 > MAX_FRAME_BYTES) {
    throw new Error('outbound IPC frame exceeds limit');
  }
  socket.write(`${frame}\n`);
  return true;
}

function chainToText(chain) {
  return chain.map((component) => {
    if (component.type === 'text') {
      return stringValue(component.data?.text);
    }
    if (component.type === 'at') {
      return stringValue(component.data?.name) || `@${stringValue(component.data?.qq)}`;
    }
    return `[${component.type}]`;
  }).join('');
}

function compactData(source, keys) {
  const result = {};
  for (const key of keys) {
    const value = source?.[key];
    if (value !== undefined && value !== null && value !== '') {
      result[snakeCase(key)] = typeof value === 'bigint' ? value.toString() : value;
    }
  }
  return result;
}

function snakeCase(value) {
  return value.replace(/[A-Z]/g, (letter) => `_${letter.toLowerCase()}`);
}

function stringValue(value) {
  if (value === undefined || value === null) {
    return '';
  }
  return String(value);
}

function numberValue(value) {
  const parsed = Number(value);
  return Number.isFinite(parsed) ? parsed : 0;
}

function parseFrameLimit(value) {
  if (value === undefined || value === '') {
    return DEFAULT_MAX_FRAME_BYTES;
  }
  const parsed = Number(value);
  if (!Number.isInteger(parsed) || parsed < 4096) {
    throw new Error('CINLAN_QQNT_MAX_FRAME_BYTES must be an integer >= 4096');
  }
  return parsed;
}

function parseIPCAddress(address) {
  const bracketed = /^\[([^\]]+)\]:(\d+)$/.exec(address);
  if (bracketed) {
    return { host: bracketed[1], port: Number(bracketed[2]) };
  }
  const plain = /^([^:]+):(\d+)$/.exec(address);
  if (plain) {
    return { host: plain[1], port: Number(plain[2]) };
  }
  return { host: '', port: 0 };
}

function reportRuntimeError(scope, error) {
  const message = error instanceof Error ? error.message : String(error);
  const previous = state.errors.get(scope);
  const now = Date.now();
  state.errors.set(scope, { message, at: now });
  if (!previous || previous.message !== message || now - previous.at >= 30000) {
    console.error(`[Cinlan QQ Runtime] ${scope}:`, message);
    publishStatus();
  }
}

function clearRuntimeError(scope) {
  if (state.errors.delete(scope)) {
    publishStatus();
  }
}

function latestRuntimeError() {
  let latestScope = '';
  let latest = null;
  for (const [scope, current] of state.errors) {
    if (!latest || current.at > latest.at) {
      latestScope = scope;
      latest = current;
    }
  }
  return latest ? `${latestScope}: ${latest.message}` : '';
}

module.exports = { start };
if (process.env.CINLAN_QQNT_TEST_EXPORTS === '1') {
  module.exports.__test = {
    state,
    handleEnvelope,
    getLoginQR,
    recordLoginQR,
    loginQRStatus,
    handleFriendRequests,
    attachFriendListener,
    approveFriend,
    buildAtElement,
    buildImageElement,
    convertElements,
    extractMultiForwardText,
    attachAVListener,
    attachLoginListener,
    attachSession,
    discoverSelfFromLoginList,
    inspectAVSDK,
    handleNativeMessage,
    publishStatus,
    quickLogin,
    recordAVEvent,
    resolveSelfUID,
    sendEnvelope,
    getGroupMemberInfo,
    observeSendUpdates,
    observeRichMediaDownload,
    prepareInboundImages,
    sendPrivateFile,
    inspectSendImage,
    resolveAllowedSendImage,
    summarizeAVArgument,
    transcribePttMessage,
    updateSelf,
  };
}
