'use strict';

const net = require('node:net');
const fs = require('node:fs');
const path = require('node:path');

const PROTOCOL_VERSION = 1;
const DEFAULT_MAX_FRAME_BYTES = 1024 * 1024;
const MAX_FRAME_BYTES = parseFrameLimit(
  process.env.CINLAN_QQNT_MAX_FRAME_BYTES,
);
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
const ELEMENT_ONLINE_FILE = 23;
const AT_UNKNOWN = 0;
const AT_ALL = 1;
const AT_ONE = 2;

const state = {
  started: false,
  socket: null,
  readBuffer: '',
  reconnectDelay: 500,
  wrapper: null,
  loginService: null,
  loginListenerID: null,
  autoLoginStarted: false,
  session: null,
  msgService: null,
  msgListenerID: null,
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
};

async function start() {
  if (state.started) {
    return;
  }
  state.started = true;
  state.bootTime = Date.now() / 1000;
  connectIPC();
  await loadWrapper();
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
        capabilities: ['message_event', 'send_message', 'send_file', 'runtime_status'],
      },
    });
    publishStatus();
  });
  socket.on('data', consumeData);
  socket.on('error', (error) => {
    console.error('[Cinlan QQ Runtime] IPC error:', error.message);
  });
  socket.on('close', () => {
    if (state.socket === socket) {
      state.socket = null;
      state.readBuffer = '';
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
    Promise.resolve(handleAction(envelope.payload || {}))
      .then((payload) => {
        sendEnvelope({
          type: 'action_result',
          id: envelope.id,
          ok: true,
          payload,
        });
      })
      .catch((error) => {
        sendEnvelope({
          type: 'action_result',
          id: envelope.id,
          ok: false,
          error: error instanceof Error ? error.message : String(error),
        });
      });
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
      onQRCodeLoginSucceed: (info) => updateSelf(info),
      onLoginConnected: () => {
        void discoverSelfFromLoginList(true);
      },
      onLoginState: (...args) => findAndUpdateSelf(args),
      onLoginRecordUpdate: (...args) => findAndUpdateSelf(args),
      onUserLoggedIn: (uin) => {
        if (uin !== undefined && uin !== null) {
          updateSelf({ uin: String(uin) });
        }
      },
      onLogoutSucceed: () => {
        state.self = { uin: '', uid: '', nick: '' };
        publishStatus('waiting_login');
      },
    });
    state.loginListenerID = service.addKernelLoginListener(listener);
    state.loginService = service;
    clearRuntimeError('login_attach');
    void discoverSelfFromLoginList();
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
    if (automatic.length === 1) {
      updateSelf(automatic[0]);
    } else if (entries.length === 1) {
      updateSelf(entries[0]);
    }
  } catch (error) {
    reportRuntimeError('login_discovery', error);
  }
}

function attachSession() {
  attachLoginListener();
  if (state.msgService) {
    if (state.self.uin) {
      void resolveSelfUID();
      publishStatus('ready');
    }
    return;
  }
  try {
    const sessionFactory = state.wrapper?.NodeIQQNTWrapperSession;
    if (!sessionFactory) {
      return;
    }
    const session = sessionFactory.getNTWrapperSession('nt_1');
    const msgService = session?.getMsgService?.();
    if (!msgService) {
      return;
    }
    const listener = listenerProxy({
      onRecvMsg: (messages) => {
        if (!Array.isArray(messages)) {
          return;
        }
        for (const message of messages) {
          try {
            handleNativeMessage(message);
            clearRuntimeError('message_event');
          } catch (error) {
            reportRuntimeError('message_event', error);
          }
        }
      },
      onAddSendMsg: (message) => {
        if (message?.senderUin) {
          updateSelf({
            uin: String(message.senderUin),
            uid: String(message.senderUid || ''),
            nick: String(message.sendNickName || ''),
          });
        }
      },
    });
    state.msgListenerID = msgService.addKernelMsgListener(listener);
    state.session = session;
    state.msgService = msgService;
    clearRuntimeError('session_attach');
    void resolveSelfUID();
    publishStatus(state.self.uin ? 'ready' : 'waiting_login');
  } catch (error) {
    state.session = null;
    state.msgService = null;
    reportRuntimeError('session_attach', error);
  }
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

function handleNativeMessage(raw) {
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
      if (numberValue(text.atType) === AT_UNKNOWN) {
        if (stringValue(text.content)) {
          chain.push({ type: 'text', data: { text: stringValue(text.content) } });
        }
      } else {
        let target = stringValue(text.atUid || text.atNtUid);
        if (
          state.self.uid &&
          (target === state.self.uid || stringValue(text.atNtUid) === state.self.uid)
        ) {
          target = state.self.uin;
        }
        if (numberValue(text.atType) === AT_ALL) {
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
      return { type: 'record', data: compactData(element.pttElement, ['fileName', 'filePath', 'duration']) };
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
    case 'get_login_list':
      return getLoginList();
    case 'quick_login':
      return quickLogin(params);
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
  updateSelf(account);
  attachSession();
  return {
    result: stringValue(result?.result),
    requested_uin: uin,
  };
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
    return sendOnlineFile(peer, fileComponents[0]);
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
    throw new Error(`native v1 cannot send component ${JSON.stringify(component?.type)}`);
  }
  if (elements.length === 0 || (elements.length === 1 && elements[0].elementType === ELEMENT_REPLY)) {
    throw new Error('send_message contains no text');
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

async function sendOnlineFile(peer, component) {
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
    elementType: ELEMENT_ONLINE_FILE,
    elementId: '',
    fileElement: {
      fileName: actualFileName,
      filePath,
      fileSize: String(stat.size),
    },
  };
  const startTime = Math.floor(Date.now() / 1000) - 2;
  let sendError;
  let lastPollError;
  try {
    const sendResult = state.msgService.sendMsg('0', peer, [fileElement], new Map());
    Promise.resolve(sendResult).catch((error) => {
      sendError = error;
    });
  } catch (error) {
    throw new Error(`QQNT file send failed: ${error.message}`);
  }

  for (let attempt = 0; attempt < 10; attempt += 1) {
    await sleep(1000);
    if (sendError) {
      throw new Error(`QQNT file send failed: ${sendError.message || String(sendError)}`);
    }
    try {
      const response = await state.msgService.getOnlineFileMsgs(peer);
      const messages = Array.isArray(response?.msgList) ? response.msgList : [];
      const found = messages.find((current) => {
        if (numberValue(current?.msgTime) < startTime) {
          return false;
        }
        return (Array.isArray(current?.elements) ? current.elements : []).some((element) => {
          if (numberValue(element?.elementType) !== ELEMENT_ONLINE_FILE || !element.fileElement) {
            return false;
          }
          return stringValue(element.fileElement.fileName) === actualFileName &&
            normalizePath(element.fileElement.filePath) === normalizePath(filePath);
        });
      });
      if (found) {
        return {
          result: 0,
          message_id: stringValue(found.msgId || found.messageId),
        };
      }
    } catch (error) {
      lastPollError = error;
    }
  }
  const detail = lastPollError
    ? `: ${lastPollError.message || String(lastPollError)}`
    : '';
  throw new Error(`QQNT file send timed out while waiting for the online-file message${detail}`);
}

function normalizePath(value) {
  return path.normalize(String(value)).toLowerCase();
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

function publishStatus(forcedState) {
  sendEnvelope({
    type: 'runtime_status',
    payload: {
      state: forcedState || runtimeState(),
      self_id: state.self.uin,
      self_uid: state.self.uid,
      nickname: state.self.nick,
      wrapper_loaded: Boolean(state.wrapper),
      session_attached: Boolean(state.msgService),
      last_error: latestRuntimeError(),
    },
  });
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
    last_error: latestRuntimeError(),
  };
}

function sendEnvelope(envelope) {
  const socket = state.socket;
  if (!socket || socket.destroyed || !socket.writable) {
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
    buildAtElement,
    convertElements,
    quickLogin,
    resolveSelfUID,
    sendOnlineFile,
    updateSelf,
  };
}
