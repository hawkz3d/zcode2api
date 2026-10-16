// 极简 CDP 客户端 —— 只用 Node 原生 WebSocket（v22+ 自带），零 npm 依赖。
// 用法： const c = await CDP.connect(wsUrl); await c.send('Page.navigate', {url});

export class CDP {
  constructor(ws) {
    this.ws = ws;
    this._id = 0;
    this._pending = new Map();   // id -> {resolve,reject,timer}
    this._listeners = new Map(); // method -> [fn]
    this.closed = false;
    ws.addEventListener('message', (ev) => this._onMessage(ev.data));
    ws.addEventListener('close', () => { this.closed = true; this._failAll('ws closed'); });
    ws.addEventListener('error', () => { /* close 事件随后到达 */ });
  }

  static async connect(wsUrl, {timeoutMs = 10000} = {}) {
    const ws = new WebSocket(wsUrl);
    await new Promise((resolve, reject) => {
      const t = setTimeout(() => reject(new Error('CDP connect timeout: ' + wsUrl)), timeoutMs);
      ws.addEventListener('open', () => { clearTimeout(t); resolve(); }, {once: true});
      ws.addEventListener('error', (e) => { clearTimeout(t); reject(new Error('CDP connect error: ' + wsUrl)); }, {once: true});
    });
    return new CDP(ws);
  }

  _onMessage(data) {
    let msg;
    try { msg = JSON.parse(data); } catch { return; }
    if (msg.id !== undefined) {
      const p = this._pending.get(msg.id);
      if (!p) return;
      this._pending.delete(msg.id);
      clearTimeout(p.timer);
      if (msg.error) p.reject(new Error(`${msg.error.code} ${msg.error.message}`));
      else p.resolve(msg.result);
      return;
    }
    const fns = this._listeners.get(msg.method);
    if (fns) for (const fn of [...fns]) { try { fn(msg.params, msg.sessionId); } catch {} }
    const all = this._listeners.get('*');
    if (all) for (const fn of [...all]) { try { fn(msg); } catch {} }
  }

  _failAll(reason) {
    for (const [, p] of this._pending) { clearTimeout(p.timer); p.reject(new Error(reason)); }
    this._pending.clear();
  }

  send(method, params = {}, {sessionId, timeoutMs = 30000} = {}) {
    if (this.closed) return Promise.reject(new Error('CDP closed'));
    const id = ++this._id;
    const payload = {id, method, params};
    if (sessionId) payload.sessionId = sessionId;
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        this._pending.delete(id);
        reject(new Error(`CDP timeout ${timeoutMs}ms on ${method}`));
      }, timeoutMs);
      this._pending.set(id, {resolve, reject, timer});
      this.ws.send(JSON.stringify(payload));
    });
  }

  on(method, fn) {
    if (!this._listeners.has(method)) this._listeners.set(method, []);
    this._listeners.get(method).push(fn);
    return () => {
      const a = this._listeners.get(method) || [];
      const i = a.indexOf(fn);
      if (i >= 0) a.splice(i, 1);
    };
  }

  once(method, predicate, timeoutMs = 30000) {
    return new Promise((resolve, reject) => {
      const off = this.on(method, (params) => {
        if (!predicate || predicate(params)) { off(); clearTimeout(t); resolve(params); }
      });
      const t = setTimeout(() => { off(); reject(new Error(`CDP wait timeout ${timeoutMs}ms for ${method}`)); }, timeoutMs);
    });
  }

  close() { try { this.ws.close(); } catch {} }
}

/** 轮询 HTTP 端点直到 CDP 就绪，返回 webSocketDebuggerUrl。 */
export async function waitForCdp(port, {timeoutMs = 30000, path = '/json/version'} = {}) {
  const deadline = Date.now() + timeoutMs;
  let lastErr = null;
  while (Date.now() < deadline) {
    try {
      const r = await fetch(`http://127.0.0.1:${port}${path}`, {signal: AbortSignal.timeout(2000)});
      if (r.ok) {
        const j = await r.json();
        if (path === '/json/version') return j.webSocketDebuggerUrl;
        return j;
      }
      lastErr = new Error(`HTTP ${r.status}`);
    } catch (e) { lastErr = e; }
    await new Promise((r) => setTimeout(r, 300));
  }
  throw new Error(`CDP not ready on :${port} (${lastErr?.message})`);
}
