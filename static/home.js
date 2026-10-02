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
// 后端契约（handler/game.go 尚未实现，实现后把 USE_MOCK 改成 false 即可）：
//   POST /api/room/create  body: {}                       -> { room_id, host_id, players[] }
//   POST /api/room/join    body: { room_id }              -> { room_id, host_id, players[], game? }
//                                                             失败: 404 房间不存在 / 409 房间已满 / 409 已在房间中
//   GET  /api/room/:id                                    -> { room_id, host_id, players[], game_id?, status?, game? }
//                                                             / 404 房间已解散
//   POST /api/room/leave   body: { room_id }              -> { ok: true }
//   POST /api/game/start   body: { room_id }              -> { game_id, players[], ... }
//                                                             必须幂等：同一房间重复调用返回同一局
//   players[] = [{ id, username, seat }]，seat: 0=东 1=南 2=西 3=北
//
// 开局约定：满 4 人由后端开局，GET /api/room/:id 会开始返回 game_id/status/game，前端只负责展示；
//           若后端不在满员时自动开局，前端会在满员 START_GRACE 毫秒后兜底调一次 /api/game/start。
//
const USE_MOCK = true;              // 后端房间接口就绪后改为 false
const MAX_PLAYERS = 4;              // 满 4 人立即开局
const POLL_INTERVAL = 2000;         // 房间状态轮询间隔(ms)
const START_GRACE = 3000;           // 满员后等待后端自行开局的缓冲时间(ms)
const MOCK_CHANNEL = 'majspirit-room';
const ROOM_TTL = 2 * 60 * 60 * 1000; // mock 房间 2 小时过期

const ROOM_API = {
  create: '/api/room/create',
  join: '/api/room/join',
  state: id => `/api/room/${id}`,
  leave: '/api/room/leave',
  start: '/api/game/start',
};

const SEATS = ['东', '南', '西', '北'];

const roomState = {
  roomId: null,
  hostId: null,
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
    players: Array.isArray(r.players) ? r.players : [],
    game_id: r.game_id ?? r.gameId ?? null,
    status: r.status ?? null,
    game: r.game ?? null,
  };
}

async function apiFetchRoom(roomId) {
  const res = await fetch(ROOM_API.state(roomId));
  if (res.status === 404) return null; // 房间已解散
  let data = null;
  try { data = await res.json(); } catch (_) { /* 忽略 */ }
  if (!res.ok) throw new Error((data && data.error) || `请求失败（${res.status}）`);
  return normalizeRoom(data);
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
  roomState.hostId = r.host_id;
  roomState.players = r.players;
  roomState.starting = false;
  roomState.started = false;
  roomState.startRequested = false;
  roomState.fullSince = 0;

  closeModal();
  document.getElementById('gamePanel').classList.add('hidden');
  document.getElementById('gameRoom').classList.remove('hidden');
  renderRoom();
  bindChannel();
  startRoomPolling();
  await syncRoom().catch(() => {}); // 立刻同步一次（可能是第 4 人加入）
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

  const status = document.getElementById('roomStatus');
  if (roomState.starting || roomState.started) return; // 开局中/已开局，不覆盖状态文案
  if (players.length >= MAX_PLAYERS) {
    status.textContent = '人数已满，正在开局...';
  } else {
    status.textContent = `等待其他玩家加入...（${players.length}/${MAX_PLAYERS}）`;
  }
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
  if (!roomState.roomId) return;

  const data = USE_MOCK
    ? mockReadRoom(roomState.roomId)
    : await apiFetchRoom(roomState.roomId);

  if (!data) {
    onRoomGone('房间已解散');
    return;
  }
  if (!roomState.roomId) return; // 期间已离开

  roomState.players = data.players;
  if (roomState.hostId == null) roomState.hostId = data.host_id;

  // 已经开局：所有人只读展示，不再重复请求开局
  const gameId = data.game_id ?? data.gameId ?? null;
  if (gameId || data.status === 'playing') {
    roomState.started = true;
    roomState.starting = false;
    renderRoom();
    showGameInfo(data.game || { game_id: gameId, room_id: roomState.roomId, players: roomState.players });
    document.getElementById('roomStatus').textContent = '对局已开始！';
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
  if (!id) return;

  const isHost = currentUser && sameId(roomState.hostId, currentUser.id);
  if (isHost && !confirm('你是房主，退出将解散房间，确定吗？')) return;

  try {
    if (USE_MOCK) mockLeaveRoom(id);
    else await apiPost(ROOM_API.leave, { room_id: id });
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
  roomState.roomId = null;
  roomState.hostId = null;
  roomState.players = [];
  roomState.starting = false;
  roomState.started = false;
  roomState.startRequested = false;
  roomState.fullSince = 0;

  document.getElementById('gameRoom').classList.add('hidden');
  document.getElementById('gamePanel').classList.add('hidden');
  document.getElementById('playerList').innerHTML = '';
  document.getElementById('playerCount').textContent = '1';
  document.getElementById('roomCode').textContent = '----';
  document.getElementById('roomStatus').textContent = '等待其他玩家加入...';
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

// 关闭页面：把自己从 mock 房间里摘掉，避免留下幽灵玩家
window.addEventListener('beforeunload', () => {
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
})();

/*待完成：
    /api/room/create  创建房间
    /api/room/join    加入房间
    /api/room/:id     房间状态
    /api/room/leave   离开房间
    /api/game/start   开始对局（当前由 /api/room 满 4 人自动触发）
    /api/history
    /api/history/:id
    —— 后端实现后把 home.js 顶部 USE_MOCK 改为 false 即可
*/
