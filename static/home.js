// ============ 弹窗控制 ============
const mask = document.getElementById('modalMask');

function openModal(id) {
  mask.classList.remove('hidden');
  document.querySelectorAll('.modal').forEach(m => m.classList.add('hidden'));
  document.getElementById(id).classList.remove('hidden');
}

function closeModal(e) {
  // 点击遮罩本身才关闭，点弹窗内部不关
  if (e && e.target !== mask) return;
  mask.classList.add('hidden');
  document.querySelectorAll('.modal').forEach(m => m.classList.add('hidden'));
}

// 按 ESC 关闭
document.addEventListener('keydown', e => {
  if (e.key === 'Escape') closeModal();
});

// ============ 小工具 ============
function escapeHtml(s) {
  return String(s ?? '').replace(/[&<>"']/g, c =>
    ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

function sameId(a, b) {
  return String(a) === String(b);
}

// 统一 POST：非 2xx 时抛出后端返回的 error 文案
async function apiPost(url, body) {
  const res = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: body ? JSON.stringify(body) : undefined,
  });
  let data = null;
  try { data = await res.json(); } catch (_) { /* 可能没有 body */ }
  if (!res.ok) throw new Error((data && data.error) || `请求失败（${res.status}）`);
  return data || {};
}

// ============ 个人资料 ============
async function openProfile() {
  openModal('profileModal');
  try {
    const res = await fetch('/api/me');
    if (!res.ok) throw new Error('未登录');
    const user = await res.json();
    document.getElementById('pUsername').textContent = user.username ?? '-';
    document.getElementById('pId').textContent = user.id ?? '-';
    document.getElementById('pPoint').textContent = user.point ?? '-';
    document.getElementById('pCreated').textContent =
      user.created_at ? new Date(user.created_at).toLocaleString() : '-';
  } catch (err) {
    document.getElementById('pUsername').textContent = '加载失败';
  }
}

// ============ 设置 / 退出 ============
function openSettings() {
  openModal('settingsModal');
}

async function logout() {
  await fetch('/api/logout', { method: 'POST' });
  window.location.href = '/login';
}

// ============================================================
// ============ 房间：创建 / 加入 / 状态同步 ==================
// ============================================================
//
// 后端契约（main.go 里还缺 GET /api/room/:id、POST /api/room/leave、POST /api/game/start 三个路由；
//           补齐后把 USE_MOCK 改成 false 即可）：
//   POST /api/room/create  body: {}                       -> { room_id, host_id, game_rule, players[] }
//   POST /api/room/join    body: { room_id }              -> { room_id, host_id, game_rule, players[], game? }
//                                                             失败: 404 房间不存在 / 409 房间已满 / 409 已在房间中
//   GET  /api/room/:id                                    -> { room_id, host_id, game_rule, players[],
//                                                              game_id?, status?, game? } / 404 房间已解散
//   POST /api/room/leave   body: { room_id }              -> { ok: true }
//   POST /api/game/start   body: { room_id }              -> { game_id, players[], ... }
//                                                             必须幂等：同一房间重复调用返回同一局
//   players[]  = [{ id, username, seat }]，seat: 0=东 1=南 2=西 3=北
//   game_rule  = model.GameRule 结构体，下发为 JSON 对象（键名与下面的 RULE_FIELDS 一致）
//
// 规则约定：房主可改规则，但现阶段只存在房主本地（修改不发请求、也不影响对局），
//           所以后端房间里的 game_rule 始终是出厂值；房主/加入者只是把它显示出来。
//
// 开局约定：满 4 人由后端开局，GET /api/room/:id 会开始返回 game_id/status/game，前端只负责展示；
//           若后端不在满员时自动开局，前端会在满员 START_GRACE 毫秒后兜底调一次 /api/game/start。
//
const USE_MOCK = false;              // 后端房间接口就绪后改为 false
const MAX_PLAYERS = 4;              // 满 4 人立即开局
const POLL_INTERVAL = 2000;         // 房间状态轮询间隔(ms)
const START_GRACE = 3000;           // 满员后等待后端自行开局的缓冲时间(ms)
const MOCK_CHANNEL = 'majspirit-room';
const ROOM_TTL = 2 * 60 * 60 * 1000; // mock 房间 2 小时过期

// WebSocket：进入房间后连 /ws/room/:id，连上就停轮询，断了再退回轮询兜底
const WS_HEARTBEAT = 25000;         // 应用层心跳间隔(ms)
const WS_RETRY_BASE = 1000;         // 重连退避起点(ms)
const WS_RETRY_MAX = 10000;         // 重连退避上限(ms)
const WS_RETRY_LIMIT = 8;           // 连续失败这么多次后交给轮询，不再重连
const WS_CLOSE_NOT_IN_ROOM = 4403;  // 服务端关闭码：不在房间里
const WS_CLOSE_ROOM_GONE = 4404;    // 服务端关闭码：房间/对局不存在

// 记住自己所在的房间与对局：满 4 人开局后房间号会被释放（房间被删），
// 断线或刷新页面时要靠 gameID 找回牌局，所以两个 id 都存进 sessionStorage。
const PERSIST_ROOM_KEY = 'majspirit:my-room';
const PERSIST_GAME_KEY = 'majspirit:my-game';

const ROOM_API = {
  create: '/api/room/create',
  join: '/api/room/join',
  state: id => `/api/room/${id}`,
  leave: '/api/room/leave',
  start: '/api/game/start',
  game: id => `/api/game/${id}`,
};

// 实时通道：大厅阶段用 /ws/room/<房间号>，对局阶段用 /ws/game/<gameID>。
// 消息协议（服务端 -> 前端）：
//   {"type":"room_state","room":{...}}     房间快照：玩家/状态/规则（同 GET /api/room/:id）
//   {"type":"game_state","game":{...}}     对局公开状态：庄家/当前手/宝牌/牌河（同 GET /api/game/:id）
//   {"type":"game_started","game":{...}}   开局
//   {"type":"pong"}                        心跳应答
//   {"type":"error","error":"..."}         业务错误，随后连接会被关闭
// 前端 -> 服务端：{"type":"ping"}
// 以后后端新增消息类型时，在 onWsMessage() 的 switch 里加一个分支即可。

const SEATS = ['东', '南', '西', '北'];

// 出厂规则，与 model/model.go 的 GameRuleDefault 逐字段保持一致
// （后端暂不接收房主修改，这里只在房间数据里没有 game_rule 时兜底）
const DEFAULT_GAME_RULE = {
  players: 4,
  rounds: 4,
  time: 10,
  extra_time: 20,
  start_score: 25000,
  nol_score: 30000,
  old_type: false,
  red_dora: 3,
};

// 规则的展示顺序/名称/控件，键与 GameRuleDefault 一致
// 局数仍写进 rounds 这个数字字段：一局=1、东风=4、半庄=8
const RULE_FIELDS = [
  { key: 'players', label: '人数', type: 'select', options: [{ value: 3, label: '3 人' }, { value: 4, label: '4 人' }] },
  { key: 'rounds', label: '局数', type: 'select', options: [{ value: 1, label: '一局' }, { value: 4, label: '东风' }, { value: 8, label: '半庄' }] },
  { key: 'time', label: '出牌时间(秒)', type: 'number', min: 1, max: 300, step: 1 },
  { key: 'extra_time', label: '加时(秒)', type: 'number', min: 0, max: 300, step: 1 },
  { key: 'start_score', label: '初始点数', type: 'number', min: 0, max: 100000, step: 100 },
  // 返点：正常结束（一位）所需的最低点数
  { key: 'nol_score', label: '返点', type: 'number', min: 0, max: 100000, step: 100 },
  { key: 'old_type', label: '古役', type: 'checkbox' },
  { key: 'red_dora', label: '赤宝牌', type: 'select', options: [{ value: 0, label: '0' }, { value: 3, label: '3' }, { value: 4, label: '4' }] },
];

const roomState = {
  roomId: null,
  gameId: null,         // 对局 id：满员开局后房间被删，改用它当实时通道
  hostId: null,
  gameRule: null,        // 当前房间的规则（房主改了就只改这里，不传后端）
  players: [],
  pollTimer: null,
  channel: null,
  starting: false,      // 正在请求开局，避免重复触发
  started: false,       // 对局已开始
  startRequested: false, // 本标签页是否已发过开局请求
  fullSince: 0,         // 满员的起始时间，用于等待后端自行开局
  mySeat: null,          // 我的座位（服务端下发）
  hand: [],              // 我的手牌（门内，只有本人拿得到）
  melds: [],             // 我的副露，每组 3 张
  options: null,         // 当前可选动作（turn_options）
  claim: null,           // 鸣牌窗口：别人打出的牌，我可以碰/吃/杠/荣和/过
  table: null,           // 最近一份公开对局快照
  finished: false,       // 本局是否已结束（和了/流局）
  riichiArmed: false,    // 已按下立直，等待点一张宣言牌
};

let currentUser = null; // 当前身份，init()/ensureSelf() 填充

// ---------- 身份 ----------
async function ensureSelf() {
  if (currentUser) return currentUser;
  try {
    const res = await fetch('/api/me');
    if (res.ok) {
      const u = await res.json();
      currentUser = { id: u.id, username: u.username || '玩家' };
      return currentUser;
    }
  } catch (_) { /* 走下面的兜底身份 */ }

  // 兜底：未登录或接口异常时，给当前标签页一个临时身份，方便本地多标签页联调
  let gid = sessionStorage.getItem('majspirit:guest-id');
  if (!gid) {
    gid = 'guest-' + Math.random().toString(36).slice(2, 8);
    sessionStorage.setItem('majspirit:guest-id', gid);
  }
  currentUser = { id: gid, username: '访客-' + gid.slice(-4) };
  return currentUser;
}

// ---------- mock 存储层（localStorage + BroadcastChannel） ----------
const roomKey = id => `majspirit:room:${id}`;

function mockReadRoom(id) {
  try {
    const raw = localStorage.getItem(roomKey(id));
    if (!raw) return null;
    const room = JSON.parse(raw);
    if (!room || !Array.isArray(room.players)) return null;
    if (room.created_at && Date.now() - room.created_at > ROOM_TTL) {
      localStorage.removeItem(roomKey(id));
      return null;
    }
    return room;
  } catch (_) {
    return null;
  }
}

function mockWriteRoom(room) {
  localStorage.setItem(roomKey(room.room_id), JSON.stringify(room));
  broadcast({ type: 'update', roomId: room.room_id });
}

function mockPurgeExpired() {
  try {
    for (let i = localStorage.length - 1; i >= 0; i--) {
      const k = localStorage.key(i);
      if (!k || !k.startsWith('majspirit:room:')) continue;
      const room = mockReadRoom(k.slice('majspirit:room:'.length));
      if (!room) localStorage.removeItem(k);
    }
  } catch (_) { /* 忽略 */ }
}

function broadcast(msg) {
  if (roomState.channel) {
    try { roomState.channel.postMessage(msg); } catch (_) { /* 忽略 */ }
  }
}

async function mockCreateRoom() {
  const self = await ensureSelf();
  mockPurgeExpired();

  let id;
  do {
    id = String(Math.floor(1000 + Math.random() * 9000)); // 4 位数字房间号
  } while (localStorage.getItem(roomKey(id)));

  const room = {
    room_id: id,
    host_id: self.id,
    players: [{ id: self.id, username: self.username, seat: 0 }],
    created_at: Date.now(),
  };
  mockWriteRoom(room);
  return room;
}

async function mockJoinRoom(roomId) {
  const self = await ensureSelf();
  const room = mockReadRoom(roomId);
  if (!room) throw new Error('房间不存在或已解散');

  const already = room.players.find(p => sameId(p.id, self.id));
  if (already) return room; // 已在房间中：直接进入

  if (room.players.length >= MAX_PLAYERS) throw new Error('房间已满（4/4）');

  const used = new Set(room.players.map(p => p.seat));
  let seat = 0;
  while (used.has(seat) && seat < MAX_PLAYERS) seat++;
  room.players.push({ id: self.id, username: self.username, seat });

  // 第 4 人：由这次写入的一方直接开局，天然保证只开一局（其他标签页只读）
  if (room.players.length >= MAX_PLAYERS) Object.assign(room, buildMockGame(room));

  mockWriteRoom(room);
  return room;
}

function mockLeaveRoom(roomId) {
  const room = mockReadRoom(roomId);
  if (!room || !currentUser) return;

  if (sameId(room.host_id, currentUser.id)) {
    // 房主退出 = 解散房间
    localStorage.removeItem(roomKey(roomId));
    broadcast({ type: 'closed', roomId });
    return;
  }

  room.players = room.players.filter(p => !sameId(p.id, currentUser.id));
  if (!room.players.length) {
    localStorage.removeItem(roomKey(roomId));
    broadcast({ type: 'closed', roomId });
  } else {
    mockWriteRoom(room);
  }
}

// 满 4 人时的开局状态（mock 里写进房间对象，等价于后端标记 room.status = playing）
function buildMockGame(room) {
  const game = {
    game_id: 'mock-' + Date.now(),
    room_id: room.room_id,
    room_type: '四人日麻',
    started_at: new Date().toISOString(),
    players: room.players.map(p => ({ id: p.id, username: p.username, seat: p.seat })),
    message: '四人已满，对局开始（前端模拟：后端 /api/game/start 就绪后自动切换为真实对局）',
  };
  return { game_id: game.game_id, status: 'playing', game };
}

// ---------- 真实接口层 ----------
function normalizeRoom(data) {
  const r = (data && data.room) || data || {};
  return {
    room_id: String(r.room_id ?? r.id ?? ''),
    host_id: r.host_id ?? r.hostId ?? null,
    game_rule: r.game_rule ?? r.gameRule ?? null,
    players: Array.isArray(r.players) ? r.players : [],
    game_id: r.game_id ?? r.gameId ?? null,
    status: r.status ?? null,
    game: r.game ?? null,
  };
}

// 房间里的 game_rule 现在是 model.GameRule 结构体（后端下发 JSON 对象）；
// 这里统一转成对象，只认 GameRuleDefault 里已有的字段，缺的/坏的用出厂值补齐
function parseRule(raw) {
  let obj = raw;
  if (typeof raw === 'string') {
    try { obj = JSON.parse(raw); } catch (_) { obj = null; }
  }
  if (!obj || typeof obj !== 'object') return { ...DEFAULT_GAME_RULE };

  const rule = {};
  RULE_FIELDS.forEach(f => {
    rule[f.key] = obj[f.key] ?? DEFAULT_GAME_RULE[f.key];
  });
  return rule;
}

async function apiFetchRoom(roomId) {
  const res = await fetch(ROOM_API.state(roomId));
  // 404 = 我的房间没了；403 = 这个号现在是别人的房间（我的号被释放后又被复用了）—— 两种都不能进去
  if (res.status === 404 || res.status === 403) return null;
  let data = null;
  try { data = await res.json(); } catch (_) { /* 忽略 */ }
  if (!res.ok) throw new Error((data && data.error) || `请求失败（${res.status}）`);
  return normalizeRoom(data);
}

// 对局状态（gameID）：房间号被释放之后，断线重连/刷新页面靠它找回牌局。
// 返回的是原始快照（含庄家/宝牌/牌河等字段），不做 normalizeRoom 裁剪。
async function apiFetchGame(gameId) {
  const res = await fetch(ROOM_API.game(gameId));
  if (res.status === 404 || res.status === 403) return null; // 对局结束，或者不是我的对局
  let data = null;
  try { data = await res.json(); } catch (_) { /* 忽略 */ }
  if (!res.ok) throw new Error((data && data.error) || `请求失败（${res.status}）`);
  return data || {};
}

// ---------- 房间 / 对局的本地记忆（刷新后能找回） ----------
// 记住的两个 id：gameID 是数据库自增的，不会复用；房间号会在开局 5 秒后被释放，
// 甚至可能被别人的新房间复用 —— 所以恢复时以 gameID 为准，房间号只当兜底。
function rememberPlace() {
  try {
    if (roomState.roomId) sessionStorage.setItem(PERSIST_ROOM_KEY, roomState.roomId);
    else sessionStorage.removeItem(PERSIST_ROOM_KEY);

    // 进了一个没有对局的新房间时，必须把上一局的 gameID 清掉，否则刷新会找回旧对局
    if (roomState.gameId) sessionStorage.setItem(PERSIST_GAME_KEY, roomState.gameId);
    else sessionStorage.removeItem(PERSIST_GAME_KEY);
  } catch (_) { /* 隐私模式下 sessionStorage 可能不可用 */ }
}

function forgetPlace() {
  try {
    sessionStorage.removeItem(PERSIST_ROOM_KEY);
    sessionStorage.removeItem(PERSIST_GAME_KEY);
  } catch (_) { /* 忽略 */ }
}

function savedPlace() {
  try {
    return {
      roomId: sessionStorage.getItem(PERSIST_ROOM_KEY),
      gameId: sessionStorage.getItem(PERSIST_GAME_KEY),
    };
  } catch (_) {
    return {};
  }
}

// 对局阶段（房间号可能已经被释放）就该走 gameID 通道
function inGamePhase() { return !!roomState.gameId; }

function currentChannel() {
  if (roomState.gameId) return { name: 'game', id: roomState.gameId };
  if (roomState.roomId) return { name: 'room', id: roomState.roomId };
  return null;
}

// ---------- 大厅入口 ----------
function openGame() {
  // 已在房间：直接回到房间面板，不再弹「开始游戏」
  if (roomState.roomId) {
    document.getElementById('gameRoom').classList.remove('hidden');
    renderRoom();
    startRoomPolling();
    return;
  }
  openModal('gameModal');
}

async function makeRoom() {
  if (roomState.roomId) return;
  try {
    // 人机数量：创建房间时带上 bots，由后端补机器人座位
    const botEl = document.getElementById('botCount');
    const bots = botEl ? Number(botEl.value) || 0 : 0;
    const url = bots > 0 ? `${ROOM_API.create}?bots=${bots}` : ROOM_API.create;

    const data = USE_MOCK ? await mockCreateRoom() : await apiPost(url);
    enterRoom(data);
  } catch (err) {
    alert('创建房间失败：' + err.message);
  }
}

function joinRoom() {
  if (roomState.roomId) {
    openGame();
    return;
  }
  const input = document.getElementById('roomIdInput');
  input.value = '';
  showJoinError('');
  openModal('joinModal');
  setTimeout(() => input.focus(), 0);
}

async function confirmJoinRoom() {
  if (roomState.roomId) { closeModal(); openGame(); return; }

  const input = document.getElementById('roomIdInput');
  const btn = document.getElementById('joinBtn');
  const code = (input.value || '').trim();

  if (!/^\d{4}$/.test(code)) {
    showJoinError('请输入 4 位数字房间号');
    input.focus();
    return;
  }

  btn.disabled = true;
  showJoinError('');
  try {
    const data = USE_MOCK
      ? await mockJoinRoom(code)
      : await apiPost(ROOM_API.join, { room_id: code });
    input.value = '';
    await enterRoom(data);
  } catch (err) {
    showJoinError(err.message);
  } finally {
    btn.disabled = false;
  }
}

function showJoinError(msg) {
  const el = document.getElementById('joinError');
  if (!msg) {
    el.textContent = '';
    el.classList.add('hidden');
    return;
  }
  el.textContent = msg;
  el.classList.remove('hidden');
}

// ---------- 进入 / 渲染房间 ----------
async function enterRoom(room) {
  const r = normalizeRoom(room);
  if (!r.room_id) {
    alert('房间信息异常，请重试');
    return;
  }

  roomState.roomId = r.room_id;
  roomState.gameId = r.game_id ? String(r.game_id) : null;
  roomState.hostId = r.host_id;
  roomState.gameRule = parseRule(r.game_rule);
  roomState.players = r.players;
  roomState.starting = false;
  roomState.started = false;
  roomState.startRequested = false;
  roomState.fullSince = 0;
  rememberPlace();

  closeModal();
  document.getElementById('gamePanel').classList.add('hidden');
  document.getElementById('gameRoom').classList.remove('hidden');
  renderRoom();
  bindChannel();
  startRoomPolling();
  connectRoomWS();                   // 拿到房间号就连实时通道（mock 模式会自动跳过）
  await syncRoom().catch(() => {}); // 立刻同步一次（可能是第 4 人加入）
}

// 房间号已经被释放（开局后房间被删）时，用 gameID 把自己接回牌局：刷新页面/重连都走这里
async function enterGameRoom(roomId, game) {
  roomState.roomId = roomId || roomState.roomId || null; // 房间可能已经不在了，只用于显示房间号与退出
  roomState.gameId = String(game.game_id ?? roomState.gameId ?? '');
  roomState.players = Array.isArray(game.players) ? game.players : [];
  roomState.gameRule = parseRule(game.game_rule);
  roomState.hostId = roomState.hostId ?? null;
  roomState.starting = false;
  roomState.started = true;
  roomState.startRequested = false;
  roomState.fullSince = 0;
  rememberPlace();

  closeModal();
  document.getElementById('gameRoom').classList.remove('hidden');
  renderRoom();
  bindChannel();
  startRoomPolling();
  applyGameData(game);   // 里面会 connectRoomWS() -> 走 /ws/game/<gameID>
}

function renderRoom() {
  const ul = document.getElementById('playerList');
  const players = roomState.players;
  const selfId = currentUser ? currentUser.id : null;

  document.getElementById('roomCode').textContent = roomState.roomId || '----';

  ul.innerHTML = players.map((p, i) => {
    const isSelf = selfId != null && sameId(p.id, selfId);
    const seat = SEATS[p.seat ?? i] ?? SEATS[i] ?? '';
    return `
      <li class="${isSelf ? 'self' : ''}">
        <span>${escapeHtml(p.username || '玩家')}${isSelf ? '（我）' : ''}</span>
        <span class="seat">${seat}位</span>
      </li>
    `;
  }).join('');

  document.getElementById('playerCount').textContent = players.length;
  renderRule();

  const status = document.getElementById('roomStatus');
  if (roomState.starting || roomState.started) return; // 开局中/已开局，不覆盖状态文案
  if (players.length >= MAX_PLAYERS) {
    status.textContent = '人数已满，正在开局...';
  } else {
    status.textContent = `等待其他玩家加入...（${players.length}/${MAX_PLAYERS}）`;
  }
}

// ---------- 游戏规则：显示 + 房主本地修改 ----------
function isHost() {
  return currentUser != null && roomState.hostId != null && sameId(roomState.hostId, currentUser.id);
}

// 只读展示的文案：勾选框显示开/关，下拉显示选项名
function ruleText(f, value) {
  if (f.type === 'checkbox') return value ? '开' : '关';
  if (f.type === 'select') {
    const opt = f.options.find(o => sameId(o.value, value));
    return opt ? opt.label : value;
  }
  return value;
}

function renderRule() {
  const ul = document.getElementById('ruleList');
  if (!ul) return;

  const rule = roomState.gameRule || { ...DEFAULT_GAME_RULE };
  const editable = isHost();

  ul.innerHTML = RULE_FIELDS.map(f => {
    const value = rule[f.key];

    // 非房主：只读展示
    if (!editable) {
      return `<li><span>${f.label}</span><span class="seat">${escapeHtml(ruleText(f, value))}</span></li>`;
    }

    // 房主：行内可编辑（修改只写进 roomState.gameRule，不传后端）
    if (f.type === 'checkbox') {
      return `<li>
        <span>${f.label}</span>
        <input class="rule-input" type="checkbox" data-rule="${f.key}" ${value ? 'checked' : ''}
               onchange="onRuleChange(this)">
      </li>`;
    }

    // 人数 / 局数 / 赤宝牌：下拉选项
    if (f.type === 'select') {
      return `<li>
        <span>${f.label}</span>
        <select class="rule-input" data-rule="${f.key}" onchange="onRuleChange(this)">${f.options
          .map(o => `<option value="${o.value}"${sameId(o.value, value) ? ' selected' : ''}>${o.label}</option>`)
          .join('')}</select>
      </li>`;
    }

    return `<li>
      <span>${f.label}</span>
      <input class="rule-input" type="number" data-rule="${f.key}" value="${escapeHtml(value)}"
             min="${f.min}" max="${f.max}" step="${f.step}" onchange="onRuleChange(this)">
    </li>`;
  }).join('');
}

// 房主改动：仅更新本地规则并重渲染（暂不生效、也不往后端传）
function onRuleChange(el) {
  const key = el.dataset.rule;
  if (!key || !roomState.gameRule || !isHost()) return;

  if (el.type === 'checkbox') {
    roomState.gameRule[key] = el.checked;
  } else {
    // 下拉（人数/局数/赤宝牌）和数字输入存进去的都是数字
    const n = Number(el.value);
    roomState.gameRule[key] = Number.isFinite(n) ? n : DEFAULT_GAME_RULE[key];
  }

  renderRule();
}

// ---------- 状态同步 ----------
function bindChannel() {
  unbindChannel();
  if (!USE_MOCK || typeof BroadcastChannel === 'undefined') return;
  roomState.channel = new BroadcastChannel(MOCK_CHANNEL);
  roomState.channel.onmessage = e => {
    const msg = e.data;
    if (!msg || !sameId(msg.roomId, roomState.roomId)) return;
    if (msg.type === 'update') syncRoom().catch(() => {});
    if (msg.type === 'closed') onRoomGone('房主已退出，房间已解散');
  };
}

function unbindChannel() {
  if (roomState.channel) {
    try { roomState.channel.close(); } catch (_) { /* 忽略 */ }
    roomState.channel = null;
  }
}

function startRoomPolling() {
  stopRoomPolling();
  roomState.pollTimer = setInterval(() => {
    if (document.hidden) return; // 后台标签页不轮询
    syncRoom().catch(() => {});
  }, POLL_INTERVAL);
}

function stopRoomPolling() {
  if (roomState.pollTimer) {
    clearInterval(roomState.pollTimer);
    roomState.pollTimer = null;
  }
}

async function syncRoom() {
  if (!roomState.roomId && !roomState.gameId) return;

  if (USE_MOCK) {
    const data = mockReadRoom(roomState.roomId);
    if (!data) { onRoomGone('房间已解散'); return; }
    if (!roomState.roomId) return; // 期间已离开
    await applyRoomData(data, true);
    return;
  }

  // 对局阶段：房间号已经释放，状态改从 gameID 拉
  if (inGamePhase()) {
    const game = await apiFetchGame(roomState.gameId);
    if (!game) { onRoomGone('对局不存在或已结束'); return; }
    applyGameData(game, true);
    return;
  }

  const data = await apiFetchRoom(roomState.roomId);
  if (!data) { onRoomGone('房间已解散'); return; }
  if (!roomState.roomId) return; // 期间已离开

  await applyRoomData(data, true);
}

// 对局快照（game_state / GET /api/game/:id）刷新界面。
// fromPoll=true 表示这是轮询拿到的：实时通道已通就丢掉，避免旧数据覆盖推送。
function applyGameData(data, fromPoll) {
  if (!data) return;
  if (fromPoll && wsState.socket && wsState.socket.readyState === 1) return;

  roomState.gameId = String(data.game_id ?? roomState.gameId ?? '');
  roomState.started = true;
  roomState.starting = false;
  if (Array.isArray(data.players) && data.players.length) roomState.players = data.players;
  if (data.game_rule) roomState.gameRule = parseRule(data.game_rule);
  roomState.table = data;                     // 公开快照：牌河/宝牌/立直/谁在打
  if (data.finished) roomState.finished = true;
  // 轮询兜底时，接口会把本人的手牌/可选动作一起带回来（字段与 WS 一致）
  if (data.you) applyHand(data.you);
  if (data.options) applyTurnOptions(data.options);

  renderRoom();
  renderTable();
  showGameInfo(data);
  document.getElementById('roomStatus').textContent = '对局已开始！';
  rememberPlace();
  connectRoomWS(); // 阶段变了就切到 /ws/game/<gameID>（同一个房间/对局时是空操作）
}

// 拿到一份房间数据就按它刷新界面 —— 轮询(syncRoom)和 WebSocket(room_state)共用这一套。
// fromPoll=true 表示这是轮询拿到的：如果实时通道已经通了，就丢掉它，
// 否则一个迟到的轮询响应会把更新的推送结果覆盖回旧状态。
async function applyRoomData(data, fromPoll) {
  if (!data) return;
  if (fromPoll && wsState.socket && wsState.socket.readyState === 1) return;

  const incomingId = data.room_id ?? data.id ?? roomState.roomId;
  if (roomState.roomId && !sameId(incomingId, roomState.roomId)) return; // 不是当前房间的数据，忽略

  roomState.players = Array.isArray(data.players) ? data.players : [];
  if (roomState.hostId == null) roomState.hostId = data.host_id ?? null;
  // 这里故意不拿服务端的 game_rule 覆盖 roomState.gameRule：
  // 现阶段房主的修改只存在本地，若被覆盖会在下一次推送时丢掉。
  // 等规则真正生效（房主修改改为提交后端、后端也会下发变更）之后，再在这里补上
  //   roomState.gameRule = parseRule(data.game_rule);
  // 并让 renderRule() 跟着重渲染即可。

  // 已经开局：所有人只读展示，不再重复请求开局
  const gameId = data.game_id ?? data.gameId ?? null;
  if (gameId || data.status === 'playing') {
    if (gameId) roomState.gameId = String(gameId);
    roomState.started = true;
    roomState.starting = false;
    renderRoom();
    // 房间快照（带 status）只展示精简信息；对局快照（无 status）直接展示它本身
    showGameInfo(data.status ? (data.game || {
      game_id: gameId, room_id: roomState.roomId, players: roomState.players,
    }) : data);
    document.getElementById('roomStatus').textContent = '对局已开始！';
    rememberPlace();
    connectRoomWS(); // 有 gameID 了就切到对局通道
    return;
  }

  renderRoom();

  if (roomState.players.length < MAX_PLAYERS) {
    roomState.fullSince = 0;
    return;
  }

  // 满 4 人：正常情况下后端会立刻开局；给一个缓冲期，仍未开局才由前端兜底触发
  if (!roomState.fullSince) roomState.fullSince = Date.now();
  if (Date.now() - roomState.fullSince >= START_GRACE) await beginGame();
}

// ---------- 满 4 人：兜底开局（后端满员自动开局时不会走到这里） ----------
async function beginGame() {
  if (roomState.starting || roomState.started) return;
  if (roomState.startRequested) return; // 每个标签页对同一房间只兜底请求一次

  roomState.startRequested = true;
  roomState.starting = true;
  stopRoomPolling();
  document.getElementById('roomStatus').textContent = '人数已满，正在开局...';

  try {
    const data = await apiPost(ROOM_API.start, { room_id: roomState.roomId });
    const r = normalizeRoom(data);

    // 后端可能"接受了请求但没真的开起来"（比如建房失败、状态还是 waiting），
    // 这时不能假装开局成功，否则界面显示"对局已开始"而服务端根本没有这一局。
    if (!r.game_id && r.status !== 'playing') {
      throw new Error('后端没有开始对局');
    }

    roomState.started = true;
    roomState.starting = false;
    showGameInfo(r.game || data);
    document.getElementById('roomStatus').textContent = '对局已开始！';
  } catch (err) {
    // 失败后允许重试：重置"已请求"标记和满员计时，下个轮询周期再试一次
    roomState.starting = false;
    roomState.startRequested = false;
    roomState.fullSince = Date.now();
    document.getElementById('roomStatus').textContent = '开局失败：' + err.message + '，稍后自动重试...';
    startRoomPolling();
  }
}

// 调试面板：默认隐藏（想看原始数据时把下面的 add 去掉即可）
function showGameInfo(data) {
  const panel = document.getElementById('gamePanel');
  const info = document.getElementById('gameInfo');

  if (info) info.textContent = JSON.stringify(data, null, 2);
  if (panel) panel.classList.add('hidden');
}

// ---------- 牌桌：手牌渲染 + 操作发送 ----------
// 手牌/可选动作由服务端通过 turn_options 只发给本人（/ws/game/<gameID>）；
// 前端只负责渲染，点了什么就原样发回去，后端会重新校验。
// 前端 -> 服务端：{"type":"action","action":"discard|riichi|kan|tsumo|ryuukyoku","tile":12}

const NUMBER_TEXT = ['', '一', '二', '三', '四', '五', '六', '七', '八', '九'];

const YAKU_TEXT = {
  riichi: '立直', daburu_riichi: '双立直', ippatsu: '一发', menzenchin_tsumohou: '门清自摸',
  tanyao: '断幺九', pinfu: '平和', ippeikou: '一杯口', ryanpeikou: '两杯口', chiitoitsu: '七对子',
  toitoi: '对对和', sanankou: '三暗刻', sankantsu: '三杠子', sanshoku_doujun: '三色同顺',
  sanshoku_doukou: '三色同刻', ittsuu: '一气通贯', honitsu: '混一色', chinitsu: '清一色',
  honroutou: '混老头', honloutou: '混老头', honchantaiyaochuu: '混全带幺九',
  junchantaiyaochuu: '纯全带幺九', shousangen: '小三元', shousuushii: '小四喜',
  yakuhai_bakaze: '场风牌', yakuhai_jikaze: '自风牌', yakuhai_haku: '白', yakuhai_hatsu: '发',
  yakuhai_chun: '中', rinshan: '岭上开花', chankan: '抢杠', haitei: '海底摸月', houtei: '河底捞鱼',
  dora: '宝牌', inner_dora: '里宝牌', red_dora: '赤宝牌',
  daisangen: '大三元', daisuushii: '大四喜', tsuuiisou: '字一色', ryuuiisou: '绿一色',
  chinroutou: '清老头', suuankou: '四暗刻', suuankou_tanki: '四暗刻单骑', suukantsu: '四杠子',
  chuuren: '九莲宝灯', chuuren_poutou: '纯正九莲宝灯', kokushi_musou: '国士无双',
  kokushi_musou_juusanmen: '国士无双十三面', tenhou: '天和', chiihou: '地和', yakuman: '役满',
};

// 后端牌值 -> 素材图：static/tiles/
//   1-9 万 / 11-19 条(s) / 21-29 筒(p) / 31-37 字(1z..7z，东=1z)；x0 = 赤5 -> 0{m,s,p}.png
//   副露标记 1xx 碰 / 2xx 杠 / 3xx 吃（取低两位再解析）
const TILE_BASE = '/static/tiles/';
const SUIT_TEXT = { m: '万', s: '条', p: '筒' };
const HONOR_TEXT = { 31: '东', 32: '南', 33: '西', 34: '北', 35: '白', 36: '发', 37: '中' };

// 每个座位的牌背颜色（东=蓝、南=绿、西=红、北=黄）
const SEAT_BACK = ['blue', 'green', 'red', 'yellow'];

function tileInfo(value) {
  const raw = Math.abs(Number(value) || 0);
  const base = raw > 100 ? raw % 100 : raw; // 去掉副露标记
  const red = base > 0 && base % 10 === 0; // x0 = 赤5
  const t = red ? base - 5 : base;

  if (t >= 31 && t <= 37) {
    return { file: (t - 30) + 'z.png', text: HONOR_TEXT[t] || '?', red: false };
  }

  const suit = t <= 9 ? 'm' : t <= 19 ? 's' : 'p';
  const num = t <= 9 ? t : t <= 19 ? t - 10 : t - 20;

  if (num < 1 || num > 9) return { file: 'back.png', text: '?', red: false };

  return { file: (red ? '0' : num) + suit + '.png', text: NUMBER_TEXT[num] + SUIT_TEXT[suit], red };
}

function labelOf(value) { return tileInfo(value).text; }

// 牌面图；opt: { clickable, drawn, armed, mini, small }
// 门风跟着庄家走：庄家是东，之后按座次顺延（座位号本身不变）
function windOf(seat, dealer) {
  const d = Number(dealer) || 0;
  return ['东', '南', '西', '北'][(((seat - d) % 4) + 4) % 4] || '';
}

// round_index "100" → 东1局（[x y z] = x场 y局 z本场）
function roundName(idx) {
  const s = String(idx || '').padEnd(3, '0');
  const wind = ['东', '南', '西', '北'][Number(s[0]) - 1] || '东';
  return `${wind}${Number(s[1]) + 1}局`;
}

// 牌面值：赤5（10/20/30）归一成 5、副露标记（1xx/2xx/3xx）取低两位（后端也是这么算的）
function normValue(v) {
  let n = Math.abs(Number(v) || 0);
  if (n > 100) n %= 100; // 碰/杠/吃 标记 → 牌值
  return n % 10 === 0 ? n - 5 : n;
}

// 列表里有没有这张牌（按牌面值比较，赤5 也能对上）
function inTiles(list, v) {
  return (list || []).some(t => normValue(t) === normValue(v));
}

function tileHtml(value, opt) {
  const o = opt || {};
  const info = tileInfo(value);
  const cls = ['tile'];
  if (o.mini) cls.push('mini');
  else if (o.small) cls.push('small');
  if (o.drawn) cls.push('drawn');
  if (o.armed) cls.push('armed');
  if (o.dim) cls.push('dim');
  if (o.clickable) cls.push('clickable');
  const click = o.clickable ? ` onclick="onTileClick(${Number(value)})"` : '';
  // data-tile 存原始值（赤5 = 10/20/30）、data-norm 存牌面值：
  // 悬停时按牌面值找"同一张牌"，赤5 再用另一套样式区分
  const raw = Number(value);
  const norm = normValue(value);
  const hover = ` onmouseover="onTileHover(${norm})" onmouseout="onTileLeave()"`;
  return `<img class="${cls.join(' ')}" src="${TILE_BASE}${info.file}" alt="${info.text}"` +
    ` data-tile="${raw}" data-norm="${norm}" draggable="false"${click}${hover}>`;
}

// 悬停某张牌：把场上所有"同一张牌"高亮出来。
// 牌河里用蓝色（hl-river），手牌/副露里用金色（hl-same），赤5 再叠一个红框（hl-red）。
function highlightSameTile(norm) {
  const n = Number(norm);
  if (!n) return;
  document.querySelectorAll('#gameTable img.tile[data-norm]').forEach(el => {
    if (Number(el.dataset.norm) !== n) return;
    el.classList.add(el.closest('.seat-discards') ? 'hl-river' : 'hl-same');
    if (Number(el.dataset.tile) % 10 === 0) el.classList.add('hl-red');
  });
}

function clearTileHighlight() {
  document.querySelectorAll('#gameTable .hl-same, #gameTable .hl-river, #gameTable .hl-red')
    .forEach(el => el.classList.remove('hl-same', 'hl-river', 'hl-red'));
}

// ---------- 听牌提示 ----------

// 某张牌还能摸到几张：4 −（自己手里的 + 场上已经见到的）
function remainCount(tile) {
  const n = normValue(tile);
  let seen = 0;
  const count = list => (list || []).forEach(v => {
    if (normValue(v) === n) seen++;
  });

  count(roomState.hand);
  (roomState.melds || []).forEach(group => count(group));

  const table = roomState.table || {};
  (table.discards || []).forEach(list => count(list));
  (table.melds || []).forEach(list => (list || []).forEach(group => count(group)));
  count(table.dora_pointers); // 宝牌指示牌也是明牌

  return Math.max(0, 4 - seen);
}

function waitsText(waits) {
  if (!waits || !waits.length) return '无';
  return waits.map(w => `${labelOf(w)}(剩${remainCount(w)})`).join('、');
}

// 手牌右侧常驻显示"现在听什么"（传 text 时临时显示别的，比如悬停时的"打这张听什么"）
function renderTenpaiHint(text) {
  const el = document.getElementById('tenpaiHint');
  if (!el) return;

  if (text) {
    el.textContent = text;
    el.style.color = 'var(--gold, #d9a441)';
    return;
  }

  el.style.color = '';
  const waits = roomState.myWaits || [];
  el.textContent = waits.length ? `听 ${waitsText(waits)}` : '';
}

// 鼠标移到一张牌上：轮到我出牌 → 显示"打这张会听什么"；其他时候回到"现在听什么"
function onTileHover(norm) {
  highlightSameTile(norm);

  const opts = roomState.options || {};
  const tenpais = opts.tenpais || {};
  const waits = tenpais[String(norm)];

  if (isMyTurn() && Array.isArray(waits)) {
    renderTenpaiHint(waits.length ? `打 ${labelOf(norm)} → 听 ${waitsText(waits)}` : `打 ${labelOf(norm)} → 不听牌`);
    return;
  }

  renderTenpaiHint();
}

function onTileLeave() {
  clearTileHighlight();
  renderTenpaiHint();
}

// 牌背图（别家手牌、牌山）
function backHtml(color, extra) {
  const c = SEAT_BACK.includes(color) ? color : 'blue';
  return `<img class="tile back ${extra || ''}" src="${TILE_BASE}back_${c}.png" alt="牌背" draggable="false">`;
}

function meldLabel(meld) {
  const kind = Math.floor(Math.abs(meld[0]) / 100);
  if (kind === 2) return '杠';
  if (kind === 3) return '吃';
  return '碰';
}

function yakuText(yaku) {
  if (!yaku) return '';
  return Object.keys(yaku)
    .filter(k => k !== 'yakuman')
    .map(k => `${YAKU_TEXT[k] || k}${yaku[k] > 1 ? `×${yaku[k]}` : ''}`)
    .join('、');
}

function setTableStatus(text) {
  const el = document.getElementById('tableStatus');
  if (el) el.textContent = text || '';
}

function mySeat() {
  if (roomState.mySeat != null) return roomState.mySeat;
  if (!currentUser || !Array.isArray(roomState.players)) return null;
  const me = roomState.players.find(p => sameId(p.id, currentUser.id));
  return me && me.seat != null ? me.seat : null;
}

function isMyTurn() {
  const opts = roomState.options;
  return !!opts && sameId(opts.seat, mySeat());
}

// 服务端下发的私有手牌（连接建立时的 hello.you，或轮询里的 you）
function applyHand(you) {
  if (!you || typeof you !== 'object') return;
  roomState.hand = Array.isArray(you.hand) ? you.hand : [];
  roomState.melds = Array.isArray(you.melds) ? you.melds : [];
  if (you.seat != null) roomState.mySeat = you.seat;
  document.getElementById('gameTable').classList.remove('hidden');
  // 服务端顺便把"你还能鸣什么"带回来了（刷新/重连后按钮不会丢）
  if (you.claim) applyClaimOptions(you.claim);
  renderHand();
  renderTenpaiHint(); // 手牌变了，听牌提示跟着更新
}

// 服务端下发的可选动作（turn_options，只发给本人）
function applyTurnOptions(msg) {
  if (!msg || typeof msg !== 'object') return;

  const seat = mySeat();

  // 服务端只发给本人；座位对不上（比如刚进牌桌还没拿到座位）时以服务端为准
  if (msg.seat != null && seat != null && !sameId(msg.seat, seat)) return;

  roomState.mySeat = msg.seat != null ? msg.seat : roomState.mySeat;
  roomState.options = msg;
  roomState.claim = null; // 轮到自己了，鸣牌窗口肯定已经关了
  roomState.finished = false;
  roomState.riichiArmed = false;
  // 记下"现在听什么"：打掉刚摸到的那张之后的待牌（手牌只有轮到自己才会变，所以不会过期）
  const tenpais = msg.tenpais || {};
  const currentKey = String(normValue(msg.drawn));
  roomState.myWaits = Array.isArray(tenpais[currentKey]) ? tenpais[currentKey] : [];
  renderResult(null); // 新的一手开始，收掉上一局的结果面板
  applyHand({ seat: roomState.mySeat, hand: msg.hand, melds: msg.melds });
}

// 服务端问"这张牌你要不要鸣"（碰/吃/杠/荣和/过），只发给能鸣的人
function applyClaimOptions(msg) {
  if (!msg || typeof msg !== 'object') return;
  if (msg.seat != null && !sameId(msg.seat, mySeat())) return;

  roomState.claim = msg;
  roomState.options = null;
  roomState.riichiArmed = false;
  renderActions();

  setTableStatus(`别人打出 ${labelOf(msg.tile)}，你可以：`);
}

function renderHand() {
  const handBox = document.getElementById('tableHand');
  const meldBox = document.getElementById('tableMelds');
  const countEl = document.getElementById('handCount');
  if (!handBox || !meldBox) return;

  const hand = roomState.hand || [];
  const opts = roomState.options || {};
  const drawn = opts.drawn;
  const riichiNow = !!opts.riichi; // 已经立直：只能摸切
  const riichiTiles = opts.riichi_tiles || [];
  const clickable = isMyTurn() && !roomState.finished;

  // 刚摸到的那张后端单独发过来（没有混在排序后的手牌里），这里摆到最右边
  const handHtml = hand.map(v => tileHtml(v, {
    clickable,
    dim: riichiNow && drawn !== 0 && v !== drawn, // 立直中不能打的牌置灰
    armed: roomState.riichiArmed && inTiles(riichiTiles, v),
  })).join('');

  const drawnHtml = (drawn != null && drawn !== 0)
    ? '<span class="drawn-gap"></span>' + tileHtml(drawn, {
      clickable,
      drawn: true,
      armed: roomState.riichiArmed && inTiles(riichiTiles, drawn),
    })
    : '';

  handBox.innerHTML = (handHtml + drawnHtml) || '<span class="table-hint">等待发牌...</span>';

  meldBox.innerHTML = (roomState.melds || []).map(m =>
    `<span class="meld-group">${m.map(v => tileHtml(v, { small: true })).join('')}` +
    `<small class="meld-label">${meldLabel(m)}</small></span>`
  ).join('');

  if (countEl) {
    const total = hand.length + (drawn != null && drawn !== 0 ? 1 : 0);
    countEl.textContent = total ? `（${total} 张）` : '';
  }

  renderActions();
}

function renderActions() {
  const box = document.getElementById('tableActions');
  if (!box) return;

  if (roomState.finished) {
    box.innerHTML = '';
    return;
  }

  // 鸣牌窗口优先：别人打出的牌，我要不要碰/吃/杠/荣和/过
  if (roomState.claim) {
    renderClaimActions(box);
    return;
  }

  const opts = roomState.options;

  if (!opts || !isMyTurn()) {
    box.innerHTML = '';
    return;
  }

  const parts = [];

  if (opts.can_tsumo) parts.push('<button class="btn primary" onclick="onAction(\'tsumo\')">自摸</button>');
  if (opts.can_ryuukyoku) parts.push('<button class="btn" onclick="onAction(\'ryuukyoku\')">九种九牌</button>');

  // 立直是两步：先按「立直」进入待选，再点金框的牌打出。
  // 这里的「取消」只取消这个待选状态（还没宣言），宣言之后是不可撤销的（后端也会拒）。
  if (opts.riichi) {
    parts.push(`<span class="chip riichi-chip">已${opts.riichi === 2 ? '双立直' : '立直'}</span>`);
    parts.push('<span class="table-hint">只能摸切</span>');
  } else if (roomState.riichiArmed) {
    parts.push('<button class="btn armed" onclick="disarmRiichi()">取消</button>');
  } else if (opts.can_riichi) {
    parts.push('<button class="btn" onclick="armRiichi()">立直</button>');
  }

  (opts.kans || []).forEach(t => {
    parts.push(`<button class="btn" onclick="onAction('kan', ${Number(t)})">杠 ${labelOf(t)}</button>`);
  });

  box.innerHTML = parts.join('') || '<span class="table-hint">轮到你了，点一张手牌打出</span>';
}

// 鸣牌按钮：目标牌放大高亮 + 荣和 / 碰 / 吃（图）/ 杠 / 过
function renderClaimActions(box) {
  const claim = roomState.claim;
  const claims = claim.claims || [];
  const parts = [];

  // 被鸣的那张：放大 + 高亮，一眼看清打出来的是什么
  parts.push(`<span class="claim-target">${tileHtml(claim.tile, { small: true, drawn: true })}` +
    `<small>${escapeHtml(labelOf(claim.tile))}</small></span>`);

  if (claims.includes('ron')) parts.push('<button class="btn primary" onclick="onClaim(\'ron\', 0)">荣和</button>');
  if (claims.includes('pon')) parts.push('<button class="btn" onclick="onClaim(\'pon\', 0)">碰</button>');
  if (claims.includes('kan')) parts.push('<button class="btn" onclick="onClaim(\'kan\', 0)">杠</button>');

  // 吃：直接摆出三张牌的图（不写"吃三万四万五万"了）
  (claim.chi || []).forEach(combo => {
    const imgs = combo.map(v => tileHtml(v, { mini: true })).join('');
    parts.push(`<button class="btn chi-btn" onclick="onClaim('chi', 0, [${combo.join(',')}])">吃 ${imgs}</button>`);
  });

  parts.push('<button class="btn" onclick="onClaim(\'pass\', 0)">过</button>');

  box.innerHTML = parts.join('');
}

function onClaim(action, tile, tiles) {
  sendGameAction(action, tile, tiles);
  roomState.claim = null;      // 已经回复，先收起按钮（后端确认后会推新状态）
  renderActions();
}

function onAction(action, tile) {
  sendGameAction(action, tile);
}

function armRiichi() {
  roomState.riichiArmed = true;
  setTableStatus('立直：点一张金框的牌打出作为宣言牌（不想立直就按「取消」）');
  renderHand();
}

function disarmRiichi() {
  roomState.riichiArmed = false;
  setTableStatus('');
  renderHand();
}

function onTileClick(value) {
  if (!isMyTurn() || roomState.finished) return;

  const opts = roomState.options || {};
  const drawn = opts.drawn;

  // 已经立直：只能摸切（后端也会拦，这里只是别让人白点）
  if (opts.riichi && drawn !== 0 && Number(value) !== Number(drawn)) {
    setTableStatus(`立直中只能摸切，请打出刚摸到的那张（${labelOf(drawn)}）`);
    return;
  }

  if (roomState.riichiArmed) {
    const tiles = opts.riichi_tiles || [];
    if (!inTiles(tiles, value)) {
      setTableStatus('这张牌打出去就不听牌了，不能当立直宣言牌');
      return;
    }
    roomState.riichiArmed = false;
    sendGameAction('riichi', value);
    return;
  }

  sendGameAction('discard', value);
}

function actionText(action, tile) {
  if (action === 'discard') return `打 ${labelOf(tile)}`;
  if (action === 'riichi') return `立直（打 ${labelOf(tile)}）`;
  if (action === 'kan') return `杠 ${labelOf(tile)}`;
  if (action === 'tsumo') return '自摸';
  if (action === 'ryuukyoku') return '九种九牌';
  return action;
}

// 操作原样发回后端；后端会重新校验（前端的按钮只是提示）
function sendGameAction(action, tile, tiles) {
  const socket = wsState.socket;

  if (!socket || socket.readyState !== 1) {
    setTableStatus('实时连接已断开，操作没有发出去');
    return;
  }

  const payload = { type: 'action', action };
  if (tile != null) payload.tile = tile;
  if (Array.isArray(tiles) && tiles.length) payload.tiles = tiles; // 吃：要吃的 3 张

  try {
    socket.send(JSON.stringify(payload));
  } catch (_) {
    setTableStatus('操作发送失败');
    return;
  }

  setTableStatus('已发送：' + actionText(action, tile));
}

// 公开状态：四家围桌（对家在上、下家在右、上家在左、自己在下）+ 牌河 + 宝牌
function renderTable() {
  const panel = document.getElementById('gameTable');
  const data = roomState.table;

  if (!panel || !data) {
    if (panel) panel.classList.add('hidden');
    return;
  }

  panel.classList.remove('hidden');

  const players = data.players || [];
  const my = mySeat();

  // 局况
  const round = String(data.round_index || '');
  const windText = ['东', '南', '西', '北'][Number(round[0] || 1) - 1] || '';
  // 局数 + 本场（本场写在 round_index 的第三位，比如 "101" = 东1局1本场）
  const honba = round ? Number(round[2] || 0) : 0;
  document.getElementById('tableRound').textContent = round
    ? `${windText}${Number(round[1] || 0) + 1}局${honba ? ` ${honba}本场` : ''}`
    : '-';

  const cur = players[data.current_player];
  const turnEl = document.getElementById('tableTurn');
  turnEl.textContent = data.finished
    ? '本局结束'
    : (my != null && data.current_player === my ? '轮到你出牌' : `轮到 ${(cur && cur.username) || ('座位 ' + data.current_player)}`);
  turnEl.style.color = my != null && data.current_player === my && !data.finished ? 'var(--primary)' : '';

  document.getElementById('tableRest').textContent = `牌山 ${data.rest != null ? data.rest : '-'} 张`;
  // 宝牌按习惯显示"指示牌"（不是宝牌本身）
  const doraPointers = Array.isArray(data.dora_pointers) && data.dora_pointers.length
    ? data.dora_pointers
    : (data.outer_dora || []);
  document.getElementById('tableDora').innerHTML =
    doraPointers.map(v => tileHtml(v, { mini: true })).join('') || '-';

  // 四家：轮转方向 下家=+1、对家=+2、上家=+3
  renderSeatBox('seatTop', my == null ? null : (my + 2) % 4, data);
  renderSeatBox('seatRight', my == null ? null : (my + 1) % 4, data);
  renderSeatBox('seatLeft', my == null ? null : (my + 3) % 4, data);
  renderSeatBox('seatSelf', my, data);
}

function renderSeatBox(elId, seat, data) {
  const el = document.getElementById(elId);
  if (!el) return;

  const players = data.players || [];

  if (seat == null || !players[seat]) {
    el.className = 'seat-box empty';
    el.innerHTML = '<div class="seat-name">等待玩家</div>';
    return;
  }

  const isMe = seat === mySeat();
  const active = seat === data.current_player && !data.finished;
  const riichi = (data.riichi || [])[seat];
  const count = Number((data.hand_counts || [])[seat] || 0);
  const scores = Array.isArray(data.scores) ? data.scores : [];
  const discards = ((data.discards || [])[seat] || []).map(v => tileHtml(v, { mini: true })).join('');

  // 别家的副露（碰/吃/杠）是公开信息，画在他自己的牌河上面
  const melds = ((data.melds || [])[seat] || []).map(m =>
    `<span class="meld-group">${m.map(v => tileHtml(v, { mini: true })).join('')}</span>`
  ).join('');

  // 自己的手牌在下面明牌区，这里不画牌背
  const backs = data.finished || isMe ? '' : Array.from({ length: Math.min(count, 14) }, () => backHtml(SEAT_BACK[seat])).join('');

  el.className = `seat-box${isMe ? ' self' : ''}${active ? ' active' : ''}`;
  el.innerHTML = `
    <div class="seat-name">
      <span class="wind">${windOf(seat, data.dealer)}</span>
      <span>${escapeHtml(players[seat].username || '')}</span>
      <span class="seat-score">${scores[seat] != null ? scores[seat] : ''}</span>
      ${seat === data.dealer ? '<span class="dealer">庄</span>' : ''}
      ${isMe ? '<span class="me">你</span>' : ''}
      ${riichi ? `<span class="riichi-tag">${riichi === 2 ? '双立直' : '立直'}</span>` : ''}
      ${active ? '<span class="turn-badge">手番</span>' : ''}
    </div>
    ${backs ? `<div class="seat-backs">${backs}</div>` : ''}
    ${melds ? `<div class="seat-melds">${melds}</div>` : ''}
    <div class="seat-discards">${discards}</div>`;
}

// 中间那行提示（谁摸了/打了什么）
function setTableLog(text) {
  const el = document.getElementById('tableLog');
  if (el && text) el.textContent = text;
}

// 和了/流局的结果面板
function renderResult(html) {
  const el = document.getElementById('tableResult');
  if (!el) return;

  if (!html) {
    el.classList.add('hidden');
    el.innerHTML = '';
    return;
  }

  el.classList.remove('hidden');
  el.innerHTML = html;
}

function eventText(event) {
  if (!event || !event.action) return '';
  const players = roomState.players || [];
  const who = (players[event.seat] && players[event.seat].username) || `座位 ${event.seat}`;
  const riichi = ((roomState.table && roomState.table.riichi) || [])[event.seat];

  switch (event.action) {
    case 'draw': return `${who} 摸牌`;
    case 'discard': return riichi ? `${who} 摸切 ${labelOf(event.tile)}` : `${who} 打出 ${labelOf(event.tile)}`;
    case 'riichi': return `${who} 立直，打出 ${labelOf(event.tile)}`;
    case 'kan': return `${who} 杠 ${labelOf(event.tile)}`;
    case 'rinshan': return `${who} 摸岭上牌`;
    case 'claim': return `${who} 打出 ${labelOf(event.tile)}，等鸣牌`;
    case 'chankan': return `${who} 加杠 ${labelOf(event.tile)}，等抢杠`;
    case 'pon': return `${who} 碰 ${labelOf(event.tile)}`;
    case 'chi': return `${who} 吃 ${labelOf(event.tile)}`;
    default: return '';
  }
}

// ---------- WebSocket：房间实时通道 ----------
// 进入房间（拿到房间号）后建立连接；连上就停掉 2s 轮询，断开则恢复轮询兜底。
// mock 模式没有服务器，直接跳过。
const wsState = {
  key: null,        // 'room:1234' / 'game:g1234'：当前连的是哪个通道
  channel: null,    // 'room' | 'game'
  socket: null,
  heartbeat: null,
  retryTimer: null,
  retry: 0,
  lastError: '',    // 服务端关闭前推的 error 文案
};

function wsUrl(ch) {
  const proto = location.protocol === 'https:' ? 'wss://' : 'ws://';
  return proto + location.host + (ch.name === 'game' ? '/ws/game/' : '/ws/room/') + ch.id;
}

// 幂等：通道没变就不重复建；通道变了（比如开局后房间号被释放、改走 gameID）会自动切换
function connectRoomWS() {
  if (USE_MOCK) return; // mock 房间只存在 localStorage 里，没有服务器可连

  const ch = currentChannel();
  if (!ch) return;

  const key = ch.name + ':' + ch.id;
  if (wsState.key === key && wsState.socket) return;

  closeRoomWS();
  wsState.key = key;
  wsState.channel = ch.name;
  wsState.lastError = '';

  let socket;
  try {
    socket = new WebSocket(wsUrl(ch));
  } catch (_) {
    scheduleWsReconnect(key);
    return;
  }
  wsState.socket = socket;

  socket.onopen = () => {
    if (wsState.socket !== socket) return;
    wsState.retry = 0;
    stopRoomPolling();      // 实时通道通了，轮询可以歇了
    startWsHeartbeat(socket);
  };

  socket.onmessage = e => {
    if (wsState.socket !== socket) return;
    let msg = null;
    try { msg = JSON.parse(e.data); } catch (_) { return; }
    onWsMessage(msg);
  };

  // 出错后浏览器还会补一个 onclose，统一在 onclose 里处理
  socket.onerror = () => {};

  socket.onclose = e => {
    if (wsState.socket !== socket) return; // 已被主动关闭/切换
    wsState.socket = null;
    stopWsHeartbeat();

    // 房间号被释放（开局后删房间）或不在房间里：如果知道 gameID 就先切到对局通道
    if (e.code === WS_CLOSE_NOT_IN_ROOM || e.code === WS_CLOSE_ROOM_GONE) {
      if (wsState.channel === 'room' && roomState.gameId) {
        wsState.key = null;      // 强制按新阶段重连
        wsState.channel = null;
        connectRoomWS();
        return;
      }
      onRoomGone(wsState.lastError || '房间已解散');
      return;
    }

    if (!roomState.roomId && !roomState.gameId) return; // 已经离开

    startRoomPolling();     // 兜底：先把轮询恢复起来
    scheduleWsReconnect(key);
  };
}

function onWsMessage(msg) {
  switch (msg.type) {
    case 'room_state':
      applyRoomData(msg.room).catch(() => {});
      break;
    case 'game_state':
      // hello/推送里可能带本人的手牌与可选动作（只有本人收得到）
      if (msg.you) applyHand(msg.you);
      if (msg.options) applyTurnOptions(msg.options);
      applyGameData(msg.game || msg);           // 先更新公开状态，日志才能看出"摸切"
      if (msg.event) setTableLog(eventText(msg.event));
      break;
    case 'your_hand':
      // 没轮到自己时的私有更新（打牌后 / 立直自动摸切之后）。
      // 手牌被服务端重发了，旧的 drawn 和可选动作就作废了 —— 不然会把同一张牌画两次。
      if (msg.you) {
        roomState.options = null;
        applyHand(msg.you);
        renderHand();
      }
      break;
    case 'claim_options':
      applyClaimOptions(msg);
      break;
    case 'round_start':
      // 下一局开始：收掉结果面板、清掉旧状态
      roomState.finished = false;
      roomState.options = null;
      roomState.claim = null;
      roomState.riichiArmed = false;
      roomState.hand = [];
      roomState.melds = [];
      renderResult(null);
      renderActions();
      if (msg.game) applyGameData(msg.game);
      setTableLog(`${roundName(msg.round_index)} 开始${msg.honba ? `（${msg.honba} 本场）` : ''}`);
      break;
    case 'game_end': {
      // 终局：顺位表（按服务端给的 order 从高到低）
      roomState.finished = true;
      roomState.options = null;
      roomState.claim = null;
      renderActions();

      const names = msg.names || [];
      const scores = msg.scores || [];
      const order = Array.isArray(msg.order) && msg.order.length
        ? msg.order
        : scores.map((_, i) => i).sort((a, b) => (scores[b] || 0) - (scores[a] || 0));

      const rows = order.map((seat, i) =>
        `<div class="result-line">${i + 1} 位　${escapeHtml(names[seat] || `座位 ${seat}`)}　${scores[seat] != null ? scores[seat] : ''}</div>`
      ).join('');

      setTableLog('对局结束');
      setTableStatus('');
      renderResult(`<div class="result-title">对局结束 · 顺位</div>${rows}`);
      break;
    }
    case 'turn_options':
      applyTurnOptions(msg);
      break;
    case 'hu': {
      roomState.finished = true;
      roomState.options = null;
      roomState.claim = null;
      renderActions();

      const players = roomState.players || [];
      const nameOf = s => (players[s] && players[s].username) || `座位 ${s}`;
      const winners = Array.isArray(msg.winners) && msg.winners.length
        ? msg.winners
        : [{ seat: msg.seat, yaku: msg.yaku, fu: msg.fu, agari: msg.agari }];

      setTableLog(`${winners.map(w => nameOf(w.seat)).join('、')} ${msg.tsumo ? '自摸' : '荣和'} ${labelOf(msg.tile)}`);
      setTableStatus('');

      // 多响就把每个和牌家都列出来
      const LIMIT_TEXT = { mangan: '满贯', haneman: '跳满', baiman: '倍满', sanbaiman: '三倍满', yakuman: '役满' };
      const deltas = msg.deltas || {};
      const scores = Array.isArray(msg.scores) ? msg.scores : [];

      const payLine = Object.keys(deltas).map(s => {
        const v = Number(deltas[s]);
        return `<span class="money ${v > 0 ? 'up' : 'down'}">${escapeHtml(nameOf(Number(s)))} ${v > 0 ? '+' : ''}${v}</span>`;
      }).join('');

      const totalLine = scores.map((v, s) => `${escapeHtml(nameOf(s))} ${v}`).join('　');

      renderResult(winners.map(w => {
        const yaku = w.yaku || {};
        const yakuman = yaku.yakuman || 0;
        const han = Object.keys(yaku).filter(k => k !== 'yakuman').reduce((n, k) => n + (yaku[k] || 0), 0);
        const limit = w.limit ? `${LIMIT_TEXT[w.limit] || ''} ` : '';
        const gain = w.points ? ` · ${limit}+${w.points}` : '';

        return `<div class="result-title">${sameId(w.seat, mySeat()) ? '你' : escapeHtml(nameOf(w.seat))} 和了！${gain}</div>` +
          `<div class="result-line">${w.agari ? '庄家' : '闲家'} · ${w.fu} 符 · ${yakuman ? yakuman + ' 倍役满' : han + ' 番'}${msg.honba ? ` · ${msg.honba} 本场` : ''}</div>` +
          `<div class="result-line">${yakuText(yaku) || '—'}</div>`;
      }).join('') +
        `<div class="result-line money-line">${payLine}</div>` +
        (totalLine ? `<div class="result-line total-line">${totalLine}</div>` : ''));
      break;
    }
    case 'ryuukyoku': {
      roomState.finished = true;
      roomState.options = null;
      roomState.claim = null;
      renderActions();

      const REASON_TEXT = {
        kyuushu_kyuuhai: '九种九牌',
        four_riichi: '四家立直',
        four_wind: '四风连打',
        four_kan: '四杠散了',
        exhausted: '荒牌平局',
        nagashi_mangan: '流局满贯',
      };
      const why = REASON_TEXT[msg.reason] || '流局';
      const roster = roomState.players || [];
      const who = s => ((roster[s] && roster[s].username) || `座位 ${s}`);
      const deltas = msg.deltas || {};
      const payLine = Object.keys(deltas).map(s => {
        const v = Number(deltas[s]);
        return `<span class="money ${v > 0 ? 'up' : 'down'}">${escapeHtml(who(Number(s)))} ${v > 0 ? '+' : ''}${v}</span>`;
      }).join('');
      const tenpaiLine = Array.isArray(msg.tenpai)
        ? `<div class="result-line">听牌：${msg.tenpai.map((t, s) => (t ? who(s) : null)).filter(Boolean).join('、') || '无（全员未听）'}</div>`
        : '';

      setTableLog(`流局：${why}`);
      setTableStatus('');
      renderResult(`<div class="result-title">流局 · ${why}</div>` + tenpaiLine +
        (payLine ? `<div class="result-line money-line">${payLine}</div>` : ''));
      break;
    }
    case 'action_error':
      setTableStatus('操作被拒绝：' + (msg.error || ''));
      break;
    case 'game_started':
      roomState.started = true;
      roomState.starting = false;
      renderRoom();
      showGameInfo(msg.game || msg);
      document.getElementById('roomStatus').textContent = '对局已开始！';
      connectRoomWS();
      break;
    case 'pong':
      break;
    case 'error':
      wsState.lastError = msg.error || ''; // 随后的 onclose 会用到
      break;
    default:
      // 以后后端新增消息类型，在这里加分支
      break;
  }
}

function scheduleWsReconnect(key) {
  if (wsState.key !== key) return;
  if (wsState.retry >= WS_RETRY_LIMIT) return; // 连续失败太多就别再敲了，交给轮询

  const delay = Math.min(WS_RETRY_MAX, WS_RETRY_BASE * Math.pow(2, wsState.retry));
  wsState.retry += 1;

  clearTimeout(wsState.retryTimer);
  wsState.retryTimer = setTimeout(() => {
    if (wsState.key === key) connectRoomWS();
  }, delay);
}

function closeRoomWS() {
  clearTimeout(wsState.retryTimer);
  wsState.retryTimer = null;
  stopWsHeartbeat();

  const socket = wsState.socket;
  wsState.socket = null;   // 先清空引用，onclose 里就不会再触发重连
  wsState.key = null;
  wsState.channel = null;
  wsState.retry = 0;
  wsState.lastError = '';

  if (socket) {
    try { socket.close(); } catch (_) { /* 忽略 */ }
  }
}

function startWsHeartbeat(socket) {
  stopWsHeartbeat();
  wsState.heartbeat = setInterval(() => {
    if (socket.readyState !== 1) return; // 1 = OPEN
    try { socket.send(JSON.stringify({ type: 'ping' })); } catch (_) { /* 忽略 */ }
  }, WS_HEARTBEAT);
}

function stopWsHeartbeat() {
  if (wsState.heartbeat) {
    clearInterval(wsState.heartbeat);
    wsState.heartbeat = null;
  }
}

// ---------- 复制房间号 ----------
async function copyRoomCode() {
  const code = roomState.roomId;
  if (!code) return;

  const btn = document.querySelector('.room-code .btn.mini');
  const reset = () => { if (btn) btn.textContent = '复制'; };

  try {
    if (navigator.clipboard && navigator.clipboard.writeText) {
      await navigator.clipboard.writeText(code);
    } else {
      legacyCopy(code);
    }
    if (btn) btn.textContent = '已复制';
    setTimeout(reset, 1200);
  } catch (_) {
    prompt('复制失败，请手动复制房间号：', code);
  }
}

function legacyCopy(text) {
  const ta = document.createElement('textarea');
  ta.value = text;
  ta.setAttribute('readonly', '');
  ta.style.position = 'fixed';
  ta.style.opacity = '0';
  document.body.appendChild(ta);
  ta.select();
  document.execCommand('copy');
  document.body.removeChild(ta);
}

// ---------- 退出 / 解散 ----------
async function leaveRoom() {
  const id = roomState.roomId;
  if (!id && !roomState.gameId) return;

  const isHost = currentUser && roomState.hostId != null && sameId(roomState.hostId, currentUser.id);
  if (isHost && !confirm('你是房主，退出将解散房间，确定吗？')) return;

  // 先断开实时通道：房主退出会让服务端以 4404 关闭所有连接，
  // 如果还连着就会弹出"房主已退出，房间已解散"的提示，而这次是我们自己主动退的。
  closeRoomWS();

  try {
    if (USE_MOCK) mockLeaveRoom(id);
    else if (id) await apiPost(ROOM_API.leave, { room_id: id }); // 房间号可能已被释放，那就只清本地
  } catch (_) { /* 退出失败也让前端回到大厅 */ }

  releaseRoom();
}

function onRoomGone(msg) {
  if (!roomState.roomId) return;
  releaseRoom();
  alert(msg);
}

function releaseRoom() {
  stopRoomPolling();
  unbindChannel();
  closeRoomWS();
  roomState.roomId = null;
  roomState.gameId = null;
  roomState.hostId = null;
  roomState.gameRule = null;
  roomState.players = [];
  roomState.starting = false;
  roomState.started = false;
  roomState.startRequested = false;
  roomState.fullSince = 0;
  roomState.mySeat = null;
  roomState.hand = [];
  roomState.melds = [];
  roomState.options = null;
  roomState.table = null;
  roomState.finished = false;
  roomState.riichiArmed = false;

  document.getElementById('gameRoom').classList.add('hidden');
  document.getElementById('gameTable').classList.add('hidden');
  document.getElementById('gamePanel').classList.add('hidden');
  document.getElementById('playerList').innerHTML = '';
  document.getElementById('ruleList').innerHTML = '';
  document.getElementById('playerCount').textContent = '1';
  document.getElementById('roomCode').textContent = '----';
  document.getElementById('roomStatus').textContent = '等待其他玩家加入...';
  forgetPlace();
}

// 页面可见性恢复时立刻同步一次
document.addEventListener('visibilitychange', () => {
  if (!document.hidden && roomState.roomId) syncRoom().catch(() => {});
});

// 多标签页回退：BroadcastChannel 不可用时靠 storage 事件
window.addEventListener('storage', e => {
  if (!roomState.roomId || !e.key) return;
  if (e.key === roomKey(roomState.roomId)) syncRoom().catch(() => {});
});

// 关闭页面：断开实时通道；mock 模式下还要把自己从房间里摘掉，避免留下幽灵玩家
window.addEventListener('beforeunload', () => {
  closeRoomWS();
  if (USE_MOCK && roomState.roomId && currentUser) mockLeaveRoom(roomState.roomId);
});

// ============ 开始游戏（旧入口，保留） ============
async function startGame() {
  const panel = document.getElementById('gamePanel');
  const info = document.getElementById('gameInfo');
  panel.classList.remove('hidden');
  info.textContent = '匹配中...';

  try {
    const res = await fetch('/api/game/start', { method: 'POST' });
    const data = await res.json();
    if (!res.ok) {
      info.textContent = '失败：' + (data.error || res.status);
      return;
    }
    info.textContent = JSON.stringify(data, null, 2);
  } catch (err) {
    info.textContent = '请求异常：' + err.message;
  }
}

// ============ 历史记录 ============
async function openHistory() {
  openModal('historyModal');
  const list = document.getElementById('historyList');
  list.innerHTML = '<p class="empty">加载中...</p>';

  try {
    const res = await fetch('/api/history');
    const data = await res.json();
    if (!res.ok) {
      list.innerHTML = `<p class="empty">加载失败：${data.error || res.status}</p>`;
      return;
    }

    const records = data.records || data || [];
    if (!records.length) {
      list.innerHTML = '<p class="empty">暂无对局记录</p>';
      return;
    }

    list.innerHTML = records.map(r => {
      const rank = r.rank ?? '-';
      const score = r.score ?? '-';
      const time = r.started_at ? new Date(r.started_at).toLocaleString() : '-';
      return `
        <div class="history-item" onclick="loadHistoryDetail(${r.game_record_id || r.id})">
          <div class="row1">
            <span>顺位：<span class="rank r${rank}">${rank} 位</span></span>
            <span>分数：${score}</span>
          </div>
          <div class="row2">${time}</div>
        </div>
      `;
    }).join('');
  } catch (err) {
    list.innerHTML = `<p class="empty">请求异常：${err.message}</p>`;
  }
}

async function loadHistoryDetail(gameId) {
  if (!gameId) return;
  try {
    const res = await fetch(`/api/history/${gameId}`);
    const data = await res.json();
    if (!res.ok) return alert('加载详情失败');
    // 详情暂时用 alert 展示，后续可换成新弹窗
    alert(JSON.stringify(data, null, 2));
  } catch (err) {
    alert('请求异常：' + err.message);
  }
}

// ============ 页面初始化：拉取用户名 ============
(async function init() {
  try {
    const res = await fetch('/api/me');
    if (res.ok) {
      const user = await res.json();
      currentUser = { id: user.id, username: user.username || '玩家' };
      document.getElementById('username').textContent = user.username || '玩家';
    }
  } catch (_) {}

  // 刷新页面后如果本地还有自己所在的 mock 房间，直接回到房间面板
  if (USE_MOCK && !roomState.roomId) {
    try {
      const self = await ensureSelf();
      const keys = Object.keys(localStorage).filter(k => k.startsWith('majspirit:room:'));
      for (const k of keys) {
        const room = mockReadRoom(k.slice('majspirit:room:'.length));
        if (room && room.players.some(p => sameId(p.id, self.id))) {
          await enterRoom(room);
          break;
        }
      }
    } catch (_) {}
  }

  // 真实模式：刷新页面后按 sessionStorage 找回自己所在的房间/对局。
  //
  // 顺序是"先对局、后房间"：gameID 是数据库自增的、永不复用，而且 /api/game/:id 会校验成员，
  // 所以它是最可靠的身份；房间号会在开局 5 秒后被释放，之后可能被别人建出同号房间，
  // 那时房间接口会 403（不是我的房间），绝不能凭号码就进去。
  if (!USE_MOCK && !roomState.roomId && !roomState.gameId) {
    const saved = savedPlace();

    if (saved.roomId || saved.gameId) {
      try {
        if (saved.gameId) {
          const game = await apiFetchGame(saved.gameId).catch(() => null);

          if (game) {
            await enterGameRoom(saved.roomId, game);
            return;
          }
        }

        if (saved.roomId) {
          const res = await fetch(ROOM_API.state(saved.roomId));

          if (res.ok) {
            await enterRoom(await res.json());
            return;
          }
          // 404/403：房间号可能已被释放并被别人的新房间占用 —— 不是我的房间，就当没在房间里
        }
      } catch (err) {
        console.warn('恢复房间/对局失败：', err); // 恢复失败就当没在房间里，但别把原因吞掉
      }

      forgetPlace();
    }
  }
})();

/*待完成：
    /api/room/create  创建房间
    /api/room/join    加入房间
    /api/room/:id     房间状态（轮询兜底）
    /api/room/leave   离开房间
    /api/game/start   开始对局（满 4 人后端自动开，前端只兜底）
    /api/game/:id     对局状态（房间号释放后，断线重连/刷新靠它）
    /ws/room/:id      大厅实时通道（通道键=房间号）
    /ws/game/:id      对局实时通道（通道键=gameID）
    /api/history
    /api/history/:id
    —— 后端实现后把 home.js 顶部 USE_MOCK 改为 false 即可
*/
