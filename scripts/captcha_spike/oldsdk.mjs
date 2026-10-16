// 决定性对照实验：把 solver.js 实际使用的【老版 SDK】(本地缓存 0022b3ef, 446KB, window.AliyunCaptcha 类式)
// 注入 headless Chromium，与官方在线【新版 SDK】(initAliyunCaptcha) 对比，判定 401 签名拒绝
// 是「headless 环境限制」还是「新版 SDK 特有」。
// 用法： node oldsdk.mjs [scene] [region] [prefix]
import { spawn } from 'node:child_process';
import { createServer } from 'node:http';
import { mkdtempSync, rmSync, readFileSync, existsSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { CDP, waitForCdp } from './cdp.mjs';

const argv = process.argv.slice(2);
const pos = argv.filter((a) => !a.startsWith('--'));
const SCENE = pos[0] || '11xygtvd';
const REGION = pos[1] || 'cn';
const PREFIX = pos[2] || 'no8xfe';

const CACHE = '/opt/.zcode-captcha-cdn-cache';
const OLD_SDK = join(CACHE, '0022b3efd90664de1a8cd8a92be74b44cedd815a');
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
  try { if (!keep) { chrome?.kill('SIGKILL'); rmSync(prof, { recursive: true, force: true }); } } catch {}
}

if (!existsSync(OLD_SDK)) { console.error('FATAL: 老版 SDK 缓存不存在 ' + OLD_SDK); process.exit(3); }
const OLD_SRC = readFileSync(OLD_SDK, 'utf8');

const HTML = `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"></head><body>
<div id="cap"></div><button id="btn">v</button>
<script>
window.AliyunCaptchaConfig = { region: ${JSON.stringify(REGION)}, prefix: ${JSON.stringify(PREFIX)} };
window.__D = { timeline: [], successArgs: null, failArgs: null, onErrorArgs: null, cbParams: null,
               initReturn: null, apiShape: null, cfgKeys: null, secTokSeen: null };
const D = window.__D; D.timeline.push({t:'boot'});
</script>
<script src="/oldsdk.js" onload="window.__D.timeline.push({t:'oldsdk-loaded'})"
  onerror="window.__D.timeline.push({t:'oldsdk-error'})"></script>
<script>
const D2 = window.__D;
function boot(){
  D2.apiShape = { hasInit: typeof window.initAliyunCaptcha, hasClass: typeof window.AliyunCaptcha,
                  clsProps: window.AliyunCaptcha ? Object.getOwnPropertyNames(window.AliyunCaptcha).slice(0,40) : null,
                  protoProps: window.AliyunCaptcha && window.AliyunCaptcha.prototype ? Object.getOwnPropertyNames(window.AliyunCaptcha.prototype).slice(0,40) : null,
                  cfg: window.AliyunCaptcha && window.AliyunCaptcha.Config ? Object.keys(window.AliyunCaptcha.Config) : null };
  D2.timeline.push({t:'api-probe', hasClass: D2.apiShape.hasClass});
  // 老版类式 API：尝试 new AliyunCaptcha(config) 与 AliyunCaptcha.init(config)
  var cfg = {
    SceneId: ${JSON.stringify(SCENE)}, region: ${JSON.stringify(REGION)}, prefix: ${JSON.stringify(PREFIX)},
    element: '#cap', button: '#btn',
    getInstance: function(i){ D2.timeline.push({t:'getInstance', has:!!i}); },
    captchaVerifyCallback: function(params, cb){
      var raw = typeof params==='string'?params:JSON.stringify(params);
      var keys=null, masked=null;
      try { var p=JSON.parse(raw); keys=Object.keys(p); masked={};
        for(var k of keys){ var v=p[k];
          masked[k]=(typeof v==='string'&&v.length>8)?(v.slice(0,4)+'…<'+v.length+'B>'):v; } }
      catch(e){ masked={parseError:e.message}; }
      D2.cbParams={type:typeof params,len:raw.length,keys:keys,masked:masked};
      D2.timeline.push({t:'verifyCallback',len:raw.length,keys:keys});
      return { captchaResult: true, bizResult: true };
    },
    success: function(v){
      var s=typeof v==='string'?v:JSON.stringify(v);
      D2.successArgs={type:typeof v,len:s.length,value:s};
      D2.timeline.push({t:'success',len:s.length});
    },
    fail: function(e){ D2.failArgs=typeof e==='string'?e:JSON.stringify(e); D2.timeline.push({t:'fail'}); },
    onError: function(e){ D2.onErrorArgs=JSON.stringify(e); D2.timeline.push({t:'onError',e:JSON.stringify(e).slice(0,200)}); },
  };
  try {
    if (typeof window.AliyunCaptcha === 'function') {
      var inst = new window.AliyunCaptcha(cfg);
      D2.initReturn = 'new:' + (inst? 'object' : String(inst));
      D2.timeline.push({t:'newed'});
    } else if (window.AliyunCaptcha && typeof window.AliyunCaptcha.init === 'function') {
      var r = window.AliyunCaptcha.init(cfg);
      D2.initReturn = 'init:' + String(r).slice(0,120);
      D2.timeline.push({t:'inited'});
    } else { D2.initReturn = 'no-usable-api'; }
  } catch(e){ D2.initReturn='throw:'+(e&&e.message); D2.timeline.push({t:'init-throw',e:String(e&&e.message).slice(0,150)}); }
}
if (document.readyState==='complete'||document.readyState==='interactive') boot();
else window.addEventListener('DOMContentLoaded', boot);
</script></body></html>`;

async function main() {
  prof = mkdtempSync(join(tmpdir(), 'capold-'));
  const port = 19700 + Math.floor(Math.random() * 200);
  const args = ['--headless=new','--no-sandbox','--disable-gpu','--disable-dev-shm-usage',
    '--disable-blink-features=AutomationControlled','--no-first-run','--no-default-browser-check',
    `--user-data-dir=${prof}`,`--remote-debugging-port=${port}`,'--remote-allow-origins=*','about:blank'];
  chrome = spawn(CHROME_BIN, args, { env: { ...process.env, LD_LIBRARY_PATH: LIBDIRS.join(':') }, stdio: ['ignore','pipe','pipe'] });
  chrome.stderr.on('data', () => {});
  const ws = await waitForCdp(port, { timeoutMs: 25000 });
  cdp = await CDP.connect(ws);
  const { targetId } = await cdp.send('Target.createTarget', { url: 'about:blank' });
  const { sessionId } = await cdp.send('Target.attachToTarget', { targetId, flatten: true });
  await cdp.send('Page.enable', {}, { sessionId });
  await cdp.send('Runtime.enable', {}, { sessionId });
  await cdp.send('Network.enable', {}, { sessionId });

  const reqs = new Map();
  cdp.on('Network.requestWillBeSent', (p, sid) => {
    if (sid !== sessionId) return;
    const u = p.request?.url || '';
    if (!/aliyun|alicdn|saf|feilin|captcha|oldsdk/i.test(u)) return;
    reqs.set(p.requestId, { url: u, method: p.request.method, postData: (p.request.postData || '').slice(0, 900), ts: el() });
  });
  cdp.on('Network.responseReceived', (p, sid) => {
    if (sid !== sessionId) return;
    const r = reqs.get(p.requestId); if (r) r.status = p.response.status;
  });
  cdp.on('Network.loadingFinished', async (p, sid) => {
    if (sid !== sessionId) return;
    const r = reqs.get(p.requestId); if (!r || r.bodyFetched) return; r.bodyFetched = true;
    try { const b = await cdp.send('Network.getResponseBody', { requestId: p.requestId }, { sessionId, timeoutMs: 8000 });
      r.bodyHead = (b.body || '').slice(0, 400); } catch {}
  });
  cdp.on('Runtime.consoleAPICalled', (p, sid) => {
    if (sid !== sessionId) return;
    const txt = (p.args||[]).map(a=>a.value??a.description??'').join(' ');
    if (/signature|401|Error|INIT_FAIL/i.test(txt)) reqs.set('c'+Math.random(), { console: p.type, text: txt.slice(0,250), ts: el() });
  });

  srv = createServer((q, res) => {
    if (q.url.startsWith('/oldsdk.js')) { res.writeHead(200, {'content-type':'application/javascript; charset=utf-8'}); res.end(OLD_SRC); return; }
    res.writeHead(200, {'content-type':'text/html; charset=utf-8'}); res.end(HTML);
  });
  await new Promise(r => srv.listen(0, '127.0.0.1', r));
  await cdp.send('Page.navigate', { url: `http://127.0.0.1:${srv.address().port}/` }, { sessionId });

  const deadline = Date.now() + 40000;
  let D = null, clicked = false;
  while (Date.now() < deadline) {
    const r = await cdp.send('Runtime.evaluate', { expression: 'JSON.stringify(window.__D||{})', returnByValue: true }, { sessionId }).catch(()=>null);
    if (r?.result?.value) { try { D = JSON.parse(r.result.value); } catch {} }
    if (D?.successArgs || D?.failArgs || D?.onErrorArgs) break;
    if (D?.initReturn && !clicked) { clicked = true;
      await cdp.send('Runtime.evaluate', { expression: `document.querySelector('#btn').click()`, returnByValue: true }, { sessionId }).catch(()=>{}); }
    await new Promise(r => setTimeout(r, 400));
  }
  await new Promise(r => setTimeout(r, 2000));

  console.log(JSON.stringify({
    sdk: 'old-cached-0022b3ef(446146B, window.AliyunCaptcha)',
    scene: SCENE, region: REGION, prefix: PREFIX, elapsedMs: Date.now() - t0,
    apiShape: D?.apiShape, initReturn: D?.initReturn,
    timeline: D?.timeline,
    successArgs: D?.successArgs ? { type: D.successArgs.type, len: D.successArgs.len } : null,
    successDecoded: decode(D?.successArgs?.value),
    failArgs: D?.failArgs, onErrorArgs: D?.onErrorArgs, cbParams: D?.cbParams,
    requests: [...reqs.values()].filter(r => r.url).map(r => ({ method: r.method, url: r.url.replace(/\?.*$/,''), status: r.status, ts: r.ts, postLen: (r.postData||'').length, postHead: (r.postData||'').slice(0,160), bodyHead: r.bodyHead })),
    consoles: [...reqs.values()].filter(r => r.console).map(r => r.console + ': ' + r.text),
  }, null, 2));
  cleanup(argv.includes('--keep'));
}

function decode(v) {
  if (!v || typeof v !== 'string') return null;
  try { const o = JSON.parse(Buffer.from(v, 'base64').toString('utf8'));
    const m = {}; for (const [k, val] of Object.entries(o)) m[k] = typeof val === 'string' && val.length > 8 ? `${val.slice(0,4)}…<${val.length}B>` : val;
    return { keys: Object.keys(o), masked: m };
  } catch (e) { return { notBase64Json: e.message, head: v.slice(0, 40) }; }
}

main().catch(e => { console.error('FATAL', e.stack || e.message); cleanup(false); process.exit(3); });
