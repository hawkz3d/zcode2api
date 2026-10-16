// 最小可复现：用真实 Chromium（CDP）跑阿里云无痕验证码，产出 VERIFY_PARAM。
//
// 设计原则（清洁室）：
//   只使用阿里云官方 SDK 的公开 API（initAliyunCaptcha / getInstance / success /
//   captchaVerifyCallback），不依赖任何逆向脚本、不依赖 npm 包（Node 原生 WebSocket）。
//
// 用法：
//   node spike.mjs [sceneId] [region] [prefix] [--timeout=40000] [--keep] [--json]
// 默认： node spike.mjs 11xygtvd cn no8xfe
//
// 退出码：
//   0  成功，stdout 有一行 VERIFY_PARAM=<base64>
//   2  求解超时
//   3  通道故障（Chromium 起不来 / CDP 连不上 / SDK 加载失败）
//   4  验证码侧失败（SDK 调用了 fail 或返回了错误码）

import { spawn } from 'node:child_process';
import { createServer } from 'node:http';
import { mkdtempSync, rmSync, writeFileSync, existsSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { CDP, waitForCdp } from './cdp.mjs';

const argv = process.argv.slice(2);
const flags = new Set(argv.filter((a) => a.startsWith('--')));
const pos = argv.filter((a) => !a.startsWith('--'));
const SCENE = pos[0] || '11xygtvd';
const REGION = pos[1] || 'cn';
const PREFIX = pos[2] || 'no8xfe';
const TIMEOUT = Number((argv.find((a) => a.startsWith('--timeout=')) || '').split('=')[1] || 40000);
const KEEP = flags.has('--keep');
const AS_JSON = flags.has('--json');

// ── 环境（本机实测得出，非猜测）──────────────────────────────────────────────
const CHROME_BIN =
  process.env.CAPTCHA_CHROME_BIN ||
  '/opt/chromium/app/vendor/runtime/usr/lib/chromium/chromium';
const CHROME_LIBDIRS = [
  '/opt/chromium/app/vendor/runtime/usr/lib/x86_64-linux-gnu',
  '/opt/chromium/app/vendor/runtime/usr/lib/x86_64-linux-gnu/ssl',
  '/opt/chromium/app/vendor/runtime/lib/x86_64-linux-gnu',
  '/opt/chromium/app/vendor/runtime/usr/lib',
  '/opt/chromium/app/vendor/runtime/lib',
];
const SDK_URL = 'https://o.alicdn.com/captcha-frontend/aliyunCaptcha/AliyunCaptcha.js';

const t0 = Date.now();
const el = (ms) => `${((Date.now() - t0) / 1000).toFixed(2)}s`;

function log(...a) { if (!AS_JSON) console.error(`[${el()}]`, ...a); }

function fail(code, msg) {
  if (AS_JSON) {
    process.stdout.write(JSON.stringify({ ok: false, code, error: msg, elapsedMs: Date.now() - t0 }) + '\n');
  } else {
    console.error(`FAIL(${code}) ${msg}`);
  }
  cleanup();
  process.exit(code);
}

// ── 一次性资源 ────────────────────────────────────────────────────────────────
let chromeProc = null;
let profileDir = null;
let httpSrv = null;
let cdp = null;
let pageSession = null;

function cleanup() {
  try { if (cdp) cdp.close(); } catch {}
  try { if (httpSrv) httpSrv.close(); } catch {}
  try { if (chromeProc && !KEEP) chromeProc.kill('SIGKILL'); } catch {}
  try { if (profileDir && !KEEP) rmSync(profileDir, { recursive: true, force: true }); } catch {}
}

// ── 本地页面 ─────────────────────────────────────────────────────────────────
const PAGE_HTML = (scene, region, prefix) => `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><title>captcha-spike</title></head>
<body>
  <div id="cap"></div>
  <button id="btn" type="button">verify</button>
  <script>
    window.AliyunCaptchaConfig = { region: ${JSON.stringify(region)}, prefix: ${JSON.stringify(prefix)} };
    window.__SPIKE = {
      stage: 'boot',
      sdkLoaded: false,
      inited: false,
      gotInstance: false,
      done: false,
      verifyParam: null,
      err: null,
      timeline: [],
      callbackParams: null,
      rawSuccessType: null,
      rawSuccessValue: null,
    };
    const S = window.__SPIKE;
    S.timeline.push({ t: 'page-script-start' });
  </script>
  <script src="${SDK_URL}" onload="window.__SPIKE.sdkLoaded=true;window.__SPIKE.timeline.push({t:'sdk-onload'})"
          onerror="window.__SPIKE.err='sdk-load-error';window.__SPIKE.timeline.push({t:'sdk-onerror'})"></script>
  <script>
    const S2 = window.__SPIKE;
    S2.timeline.push({ t: 'sdk-script-tag-done', hasInit: typeof window.initAliyunCaptcha });
    function boot() {
      if (typeof window.initAliyunCaptcha !== 'function') {
        S2.err = 'initAliyunCaptcha not a function: ' + typeof window.initAliyunCaptcha;
        S2.timeline.push({ t: 'boot-fail', err: S2.err });
        return;
      }
      S2.stage = 'init';
      try {
        window.initAliyunCaptcha({
          SceneId: ${JSON.stringify(scene)},
          region: ${JSON.stringify(region)},
          prefix: ${JSON.stringify(prefix)},
          element: '#cap',
          button: '#btn',
          getInstance: function (inst) {
            S2.gotInstance = true;
            S2.timeline.push({ t: 'getInstance', instMethods: Object.getOwnPropertyNames(Object.getPrototypeOf(inst || {})).slice(0, 40) });
          },
          captchaVerifyCallback: function (params, cb) {
            S2.callbackParams = {
              keys: Object.keys(params || {}),
              sceneId: params && params.sceneId,
              certifyId: params && params.certifyId,
              deviceTokenLen: params && params.deviceToken ? String(params.deviceToken).length : 0,
              deviceTokenHead: params && params.deviceToken ? String(params.deviceToken).slice(0, 6) : null,
              failover: params && params.failover,
            };
            S2.timeline.push({ t: 'captchaVerifyCallback' });
            // 无痕模式：本地判定通过（真实验证在服务端做，这里只负责产出产物）
            return { captchaResult: true, bizResult: true };
          },
          success: function (v) {
            S2.rawSuccessType = typeof v;
            S2.rawSuccessValue = typeof v === 'string' ? v : JSON.stringify(v);
            S2.done = true;
            S2.stage = 'success';
            S2.verifyParam = v;
            S2.timeline.push({ t: 'success' });
          },
          fail: function (e) {
            S2.err = 'fail-callback: ' + (typeof e === 'string' ? e : JSON.stringify(e));
            S2.timeline.push({ t: 'fail', err: S2.err });
          },
        }).then(function () {
          S2.inited = true;
          S2.timeline.push({ t: 'init-resolved' });
        }).catch(function (e) {
          S2.err = 'init-rejected: ' + (e && e.message ? e.message : String(e));
          S2.timeline.push({ t: 'init-rejected', err: S2.err });
        });
      } catch (e) {
        S2.err = 'init-throw: ' + (e && e.message ? e.message : String(e));
        S2.timeline.push({ t: 'init-throw', err: S2.err });
      }
    }
    if (document.readyState === 'complete' || document.readyState === 'interactive') boot();
    else window.addEventListener('DOMContentLoaded', boot);
  </script>
</body></html>`;

// ── 主流程 ───────────────────────────────────────────────────────────────────
async function main() {
  if (!existsSync(CHROME_BIN)) return fail(3, `chromium 二进制不存在: ${CHROME_BIN}`);

  profileDir = mkdtempSync(join(tmpdir(), 'captcha-spike-'));
  const port = 19200 + Math.floor(Math.random() * 300);

  log(`启动 Chromium: ${CHROME_BIN} (headless, port=${port})`);
  chromeProc = spawn(CHROME_BIN, [
    '--headless=new',
    '--no-sandbox',
    '--disable-gpu',
    '--disable-dev-shm-usage',
    '--disable-blink-features=AutomationControlled',
    '--no-first-run',
    '--no-default-browser-check',
    `--user-data-dir=${profileDir}`,
    `--remote-debugging-port=${port}`,
    '--remote-allow-origins=*',
    'about:blank',
  ], {
    env: { ...process.env, LD_LIBRARY_PATH: CHROME_LIBDIRS.join(':') },
    stdio: ['ignore', 'pipe', 'pipe'],
    detached: false,
  });
  chromeProc.stderr.on('data', () => {}); // 吞掉 dbus 噪音
  chromeProc.on('exit', (c, s) => { if (!cdp) log(`chromium 提前退出 code=${c} signal=${s}`); });

  let wsUrl;
  try {
    wsUrl = await waitForCdp(port, { timeoutMs: 25000 });
  } catch (e) {
    return fail(3, `CDP 未就绪: ${e.message}`);
  }
  log(`CDP 就绪 ${wsUrl}`);

  try {
    cdp = await CDP.connect(wsUrl);
  } catch (e) {
    return fail(3, `CDP 连接失败: ${e.message}`);
  }

  // 新建页面并 attach（flatten 会话）
  const { targetId } = await cdp.send('Target.createTarget', { url: 'about:blank' });
  const { sessionId } = await cdp.send('Target.attachToTarget', { targetId, flatten: true });
  pageSession = sessionId;

  const net = [];      // 网络事件（仅记录，用于诊断）
  const netPost = new Map();

  await cdp.send('Page.enable', {}, { sessionId });
  await cdp.send('Runtime.enable', {}, { sessionId });
  await cdp.send('Network.enable', {}, { sessionId });
  await cdp.send('Target.setAutoAttach', { autoAttach: true, waitForDebuggerOnStart: false, flatten: true });

  cdp.on('Network.requestWillBeSent', (p, sid) => {
    if (sid !== sessionId) return;
    const u = p.request?.url || '';
    if (!/aliyun|alicdn|captcha|saf/i.test(u)) return;
    net.push({ method: p.request.method, url: u.replace(/\?.*$/, ''), ts: el() });
    netPost.set(p.requestId, p.request.postData || null);
  });
  cdp.on('Network.responseReceived', (p, sid) => {
    if (sid !== sessionId) return;
    const u = p.response?.url || '';
    if (!/aliyun|alicdn|captcha|saf/i.test(u)) return;
    const rec = net.find((n) => u.startsWith(n.url));
    if (rec) rec.status = p.response.status;
  });
  cdp.on('Runtime.consoleAPICalled', (p, sid) => {
    if (sid !== sessionId) return;
    const txt = (p.args || []).map((a) => a.value ?? a.description ?? '').join(' ');
    if (/error|fail|warn/i.test(p.type)) net.push({ console: p.type, text: txt.slice(0, 200) });
  });

  // 本地 HTTP 提供页面
  httpSrv = createServer((req, res) => {
    res.writeHead(200, { 'content-type': 'text/html; charset=utf-8' });
    res.end(PAGE_HTML(SCENE, REGION, PREFIX));
  });
  await new Promise((r) => httpSrv.listen(0, '127.0.0.1', r));
  const pageUrl = `http://127.0.0.1:${httpSrv.address().port}/`;
  log(`本地页面 ${pageUrl}`);

  const loaded = cdp.once('Page.loadEventFired', null, 20000).catch(() => null);
  await cdp.send('Page.navigate', { url: pageUrl }, { sessionId });
  await loaded;
  log('页面加载完成，等待 SDK 初始化…');

  // 轮询 __SPIKE 状态
  const deadline = Date.now() + TIMEOUT;
  let state = null;
  let triggered = false;
  while (Date.now() < deadline) {
    const r = await cdp.send('Runtime.evaluate', {
      expression: 'JSON.stringify(window.__SPIKE || {err:"no __SPIKE"})',
      returnByValue: true,
    }, { sessionId }).catch(() => null);
    if (r?.result?.value) { try { state = JSON.parse(r.result.value); } catch {} }

    if (state?.done && state?.verifyParam) break;
    if (state?.err && !state?.inited) break; // 硬失败

    // 初始化完成后仍未出结果 → 主动触发验证
    if (state?.inited && !state?.done && !triggered) {
      triggered = true;
      log('init 完成但未自动触发，主动点击验证按钮…');
      await cdp.send('Runtime.evaluate', {
        expression: `(function(){var b=document.querySelector('#btn'); if(b) b.click(); return !!b;})()`,
        returnByValue: true,
      }, { sessionId }).catch(() => {});
    }
    await new Promise((r) => setTimeout(r, 300));
  }

  const elapsedMs = Date.now() - t0;
  const result = {
    ok: !!(state?.done && state?.verifyParam),
    scene: SCENE, region: REGION, prefix: PREFIX,
    elapsedMs,
    stage: state?.stage, sdkLoaded: state?.sdkLoaded, inited: state?.inited,
    gotInstance: state?.gotInstance,
    err: state?.err,
    rawSuccessType: state?.rawSuccessType,
    callbackParams: state?.callbackParams,
    verifyParamLen: state?.verifyParam ? String(state.verifyParam).length : 0,
    verifyParamHead: state?.verifyParam ? String(state.verifyParam).slice(0, 24) + '…' : null,
    verifyParamDecoded: decodeVerifyParam(state?.verifyParam),
    timeline: state?.timeline,
    network: net.slice(0, 40),
  };

  if (AS_JSON) process.stdout.write(JSON.stringify(result, null, 2) + '\n');
  else {
    console.error('── 结果 ────────────────────────────────');
    console.error(JSON.stringify({ ...result, verifyParam: undefined }, null, 2));
  }

  cleanup();

  if (result.ok) {
    if (!AS_JSON) process.stdout.write(`VERIFY_PARAM=${state.verifyParam}\n`);
    else process.stdout.write(''); // JSON 模式已输出完整对象
    return process.exit(0);
  }
  if (state?.err) return process.exit(4);
  return process.exit(2);
}

/** 解码 verifyParam（base64 JSON），字段值打码。 */
function decodeVerifyParam(v) {
  if (!v || typeof v !== 'string') return null;
  try {
    const json = Buffer.from(v, 'base64').toString('utf8');
    const o = JSON.parse(json);
    const masked = {};
    for (const [k, val] of Object.entries(o)) {
      masked[k] = typeof val === 'string' && val.length > 8
        ? `${val.slice(0, 4)}…<${val.length}B>`
        : val;
    }
    return { keys: Object.keys(o), masked };
  } catch (e) {
    return { decodeError: e.message };
  }
}

process.on('SIGINT', () => { cleanup(); process.exit(130); });
main().catch((e) => fail(3, `未捕获异常: ${e.stack || e.message}`));
