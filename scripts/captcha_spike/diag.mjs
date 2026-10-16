// 诊断：完整捕获阿里云验证码 SDK 的网络请求与响应体，定位 securityToken 来源。
// 用法： node diag.mjs [scene] [region] [prefix] [--proxy=1] [--json]
import { spawn } from 'node:child_process';
import { createServer } from 'node:http';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { CDP, waitForCdp } from './cdp.mjs';

const argv = process.argv.slice(2);
const flags = new Set(argv.filter((a) => a.startsWith('--')));
const pos = argv.filter((a) => !a.startsWith('--'));
const SCENE = pos[0] || '11xygtvd';
const REGION = pos[1] || 'cn';
const PREFIX = pos[2] || 'no8xfe';
const USE_PROXY = flags.has('--proxy');
const EXT_CDP = (argv.find((a) => a.startsWith('--cdp=')) || '').split('=')[1] || '';
const VERIFY_TYPE = (argv.find((a) => a.startsWith('--verify-type=')) || '').split('=')[1] || '';
const TIMEOUT = Number((argv.find((a) => a.startsWith('--timeout=')) || '').split('=')[1] || 30000);

const CHROME_BIN = '/opt/chromium/app/vendor/runtime/usr/lib/chromium/chromium';
const LIBDIRS = [
  '/opt/chromium/app/vendor/runtime/usr/lib/x86_64-linux-gnu',
  '/opt/chromium/app/vendor/runtime/usr/lib/x86_64-linux-gnu/ssl',
  '/opt/chromium/app/vendor/runtime/lib/x86_64-linux-gnu',
  '/opt/chromium/app/vendor/runtime/usr/lib',
  '/opt/chromium/app/vendor/runtime/lib',
];

const t0 = Date.now();
const el = () => `${((Date.now() - t0) / 1000).toFixed(2)}s`;
const log = (...a) => console.error(`[${el()}]`, ...a);

let chrome = null, prof = null, srv = null, cdp = null;
function cleanup(keep) {
  try { cdp?.close(); } catch {}
  try { srv?.close(); } catch {}
  try { if (!keep && !EXT_CDP) { chrome?.kill('SIGKILL'); rmSync(prof, { recursive: true, force: true }); } } catch {}
}

const HTML = `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"></head><body>
<div id="cap"></div><button id="btn">v</button>
<script>
window.AliyunCaptchaConfig = { region: ${JSON.stringify(REGION)}, prefix: ${JSON.stringify(PREFIX)} };
window.__D = { timeline: [], successArgs: null, failArgs: null, onErrorArgs: null, cbParams: null, initReturn: null };
const D = window.__D;
D.timeline.push({t:'boot'});
</script>
<script src="https://o.alicdn.com/captcha-frontend/aliyunCaptcha/AliyunCaptcha.js"
  onload="window.__D.timeline.push({t:'sdk-loaded'})"
  onerror="window.__D.timeline.push({t:'sdk-error'})"></script>
<script>
const D2 = window.__D;
function boot(){
  if(typeof window.initAliyunCaptcha!=='function'){ D2.timeline.push({t:'no-init', ty:typeof window.initAliyunCaptcha}); return; }
  D2.timeline.push({t:'init-call'});
  try {
    var p = window.initAliyunCaptcha({
      SceneId: ${JSON.stringify(SCENE)},
      region: ${JSON.stringify(REGION)},
      prefix: ${JSON.stringify(PREFIX)},
      ${VERIFY_TYPE ? `verifyType: ${JSON.stringify(VERIFY_TYPE)},` : ''}
      element: '#cap', button: '#btn',
      getInstance: function(inst){ D2.timeline.push({t:'getInstance', has:!!inst}); },
      captchaVerifyCallback: function(params, cb){
        // params 是 SDK 传来的 JSON 串；解析字段结构（值打码），这才是真正载有 securityToken 的产物载体
        var raw = typeof params === 'string' ? params : JSON.stringify(params);
        var parsed = null, keys = null, masked = null;
        try {
          parsed = JSON.parse(raw);
          keys = Object.keys(parsed);
          masked = {};
          for (var i = 0; i < keys.length; i++) {
            var k = keys[i], v = parsed[k];
            masked[k] = (typeof v === 'string' && v.length > 8) ? (v.slice(0,4) + '…<' + v.length + 'B>') : v;
          }
        } catch (e) { masked = { parseError: e.message }; }
        D2.cbParams = { type: typeof params, len: raw.length, head: raw.slice(0,12),
                        isArray: Array.isArray(params), ctor: params && params.constructor && params.constructor.name,
                        keys: keys, masked: masked };
        D2.timeline.push({t:'verifyCallback', len: D2.cbParams.len, keys: keys});
        return { captchaResult: true, bizResult: true };
      },
      success: function(v){
        D2.successArgs = { type: typeof v, len: v?String(v).length:0, value: typeof v==='string'? v : JSON.stringify(v) };
        D2.timeline.push({t:'success', len: D2.successArgs.len});
      },
      fail: function(e){ D2.failArgs = (typeof e==='string'? e : JSON.stringify(e)); D2.timeline.push({t:'fail'}); },
      onError: function(e){ D2.onErrorArgs = JSON.stringify(e); D2.timeline.push({t:'onError', e: JSON.stringify(e)}); },
    });
    if (p && typeof p.then === 'function') {
      p.then(function(r){ D2.initReturn = 'resolved:' + JSON.stringify(r && r.CertifyId ? {hasCertifyId:true} : r).slice(0,200); D2.timeline.push({t:'init-resolved'}); })
       .catch(function(e){ D2.initReturn = 'rejected:' + (e&&e.message?e.message:String(e)); D2.timeline.push({t:'init-rejected'}); });
    } else { D2.initReturn = 'sync:' + String(p).slice(0,200); }
  } catch(e){ D2.initReturn = 'throw:' + (e&&e.message); D2.timeline.push({t:'init-throw'}); }
}
if (document.readyState==='complete'||document.readyState==='interactive') boot();
else window.addEventListener('DOMContentLoaded', boot);
</script></body></html>`;

async function main() {
  prof = mkdtempSync(join(tmpdir(), 'capdiag-'));
  const port = 19500 + Math.floor(Math.random() * 200);
  const args = ['--headless=new','--no-sandbox','--disable-gpu','--disable-dev-shm-usage',
    '--disable-blink-features=AutomationControlled','--no-first-run','--no-default-browser-check',
    `--user-data-dir=${prof}`,`--remote-debugging-port=${port}`,'--remote-allow-origins=*','about:blank'];
  if (USE_PROXY) args.splice(2, 0, '--proxy-server=http://127.0.0.1:7890');
  let ws;
  if (EXT_CDP) {
    ws = EXT_CDP; log('复用外部 CDP: ' + ws);
  } else {
    chrome = spawn(CHROME_BIN, args, { env: { ...process.env, LD_LIBRARY_PATH: LIBDIRS.join(':') }, stdio: ['ignore','pipe','pipe'] });
    chrome.stderr.on('data', () => {});
    log('chromium 启动 port=' + port + ' proxy=' + USE_PROXY);
    ws = await waitForCdp(port, { timeoutMs: 25000 });
  }
  cdp = await CDP.connect(ws);
  const { targetId } = await cdp.send('Target.createTarget', { url: 'about:blank' });
  const { sessionId } = await cdp.send('Target.attachToTarget', { targetId, flatten: true });

  await cdp.send('Page.enable', {}, { sessionId });
  await cdp.send('Runtime.enable', {}, { sessionId });
  await cdp.send('Network.enable', { maxResourceBufferSize: 50 * 1024 * 1024, maxTotalBufferSize: 100 * 1024 * 1024 }, { sessionId });

  const reqs = new Map();   // requestId -> {url, method, postData, status, type}
  const bodies = [];
  cdp.on('Network.requestWillBeSent', (p, sid) => {
    if (sid !== sessionId) return;
    const u = p.request?.url || '';
    if (!/aliyun|alicdn|saf-captcha|feilin|captcha/i.test(u)) return;
    reqs.set(p.requestId, { url: u, method: p.request.method, postData: (p.request.postData || '').slice(0, 1200), ts: el() });
  });
  cdp.on('Network.responseReceived', async (p, sid) => {
    if (sid !== sessionId) return;
    const r = reqs.get(p.requestId);
    if (!r) return;
    r.status = p.response.status;
    r.mime = p.response.mimeType;
  });
  cdp.on('Network.loadingFinished', async (p, sid) => {
    if (sid !== sessionId) return;
    const r = reqs.get(p.requestId);
    if (!r || r.bodyFetched) return;
    r.bodyFetched = true;
    try {
      const b = await cdp.send('Network.getResponseBody', { requestId: p.requestId }, { sessionId, timeoutMs: 8000 });
      r.bodyLen = (b.body || '').length;
      r.bodyHead = (b.body || '').slice(0, 900);
      if (r.bodyLen > 0) bodies.push(r);
    } catch (e) { r.bodyErr = e.message; }
  });
  cdp.on('Runtime.consoleAPICalled', (p, sid) => {
    if (sid !== sessionId) return;
    const txt = (p.args||[]).map(a=>a.value??a.description??'').join(' ');
    reqs.set('c' + Math.random(), { console: p.type, text: txt.slice(0, 300), ts: el() });
  });

  srv = createServer((q, res) => { res.writeHead(200, {'content-type':'text/html; charset=utf-8'}); res.end(HTML); });
  await new Promise(r => srv.listen(0, '127.0.0.1', r));
  await cdp.send('Page.navigate', { url: `http://127.0.0.1:${srv.address().port}/` }, { sessionId });

  const deadline = Date.now() + TIMEOUT;
  let D = null, clicked = false;
  while (Date.now() < deadline) {
    const r = await cdp.send('Runtime.evaluate', { expression: 'JSON.stringify(window.__D||{})', returnByValue: true }, { sessionId }).catch(()=>null);
    if (r?.result?.value) { try { D = JSON.parse(r.result.value); } catch {} }
    if (D?.successArgs || D?.failArgs || D?.onErrorArgs) break;
    if (D?.initReturn && !clicked) { clicked = true; await cdp.send('Runtime.evaluate', { expression: `document.querySelector('#btn').click()`, returnByValue: true }, { sessionId }).catch(()=>{}); }
    await new Promise(r => setTimeout(r, 400));
  }
  await new Promise(r => setTimeout(r, 2500)); // 等响应体抓完

  const out = {
    scene: SCENE, region: REGION, prefix: PREFIX, proxy: USE_PROXY, extCdp: !!EXT_CDP,
    elapsedMs: Date.now() - t0,
    timeline: D?.timeline,
    successArgs: D?.successArgs ? { type: D.successArgs.type, len: D.successArgs.len } : null,
    successValueDecoded: decode(D?.successArgs?.value),
    failArgs: D?.failArgs, onErrorArgs: D?.onErrorArgs,
    initReturn: D?.initReturn,
    cbParams: D?.cbParams,
    requests: [...reqs.values()].filter(r => r.url).map(r => ({ method: r.method, url: r.url.replace(/\?.*$/,''), status: r.status, ts: r.ts, postLen: (r.postData||'').length, postHead: (r.postData||'').slice(0,200), bodyLen: r.bodyLen, bodyHead: r.bodyHead })),
    consoles: [...reqs.values()].filter(r => r.console).map(r => r.console + ': ' + r.text),
  };
  console.log(JSON.stringify(out, null, 2));
  if (EXT_CDP) { try { await cdp.send('Target.closeTarget', { targetId }); } catch {} }
  cleanup(flags.has('--keep'));
}

function decode(v) {
  if (!v || typeof v !== 'string') return null;
  try {
    const o = JSON.parse(Buffer.from(v, 'base64').toString('utf8'));
    const m = {}; for (const [k, val] of Object.entries(o)) m[k] = typeof val === 'string' && val.length > 8 ? `${val.slice(0,4)}…<${val.length}B>` : val;
    return { keys: Object.keys(o), masked: m };
  } catch (e) { return { notBase64Json: e.message, head: v.slice(0, 40) }; }
}

main().catch(e => { console.error('FATAL', e.stack || e.message); cleanup(false); process.exit(3); });
