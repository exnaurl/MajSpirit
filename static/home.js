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
    const data = USE_MOCK ? await mockCreateRoom() : await apiPost(ROOM_API.create);
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

  renderRoom();
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

function showGameInfo(data) {
  document.getElementById('gamePanel').classList.remove('hidden');
  document.getElementById('gameInfo').textContent = JSON.stringify(data, null, 2);
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
      applyGameData(msg.game);
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

  document.getElementById('gameRoom').classList.add('hidden');
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
