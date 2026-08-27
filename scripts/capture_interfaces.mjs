import fs from 'node:fs';
import http from 'node:http';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { createRequire } from 'node:module';

const require = createRequire(import.meta.url);
let playwright;
for (const name of ['playwright', 'playwright-core']) {
  try { playwright = require(name); break; } catch {}
}
if (!playwright) throw new Error('Playwright is required to capture interface screenshots');
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const webRoot = path.join(root, 'web');
const mime = {'.html':'text/html; charset=utf-8','.css':'text/css','.js':'text/javascript','.png':'image/png','.svg':'image/svg+xml','.json':'application/json'};
const server = http.createServer((req,res)=>{
  const u = new URL(req.url, 'http://127.0.0.1');
  if (u.pathname === '/meta') { res.setHeader('content-type','application/json'); res.end(JSON.stringify({measurementProtocolVersion:2,maxTransferBytes:1073741824,uploadReceiptVersion:1,packetFrameVersion:2,serverName:'Cascade Edge 01'})); return; }
  if (u.pathname === '/__down') { const n=Math.min(+(u.searchParams.get('bytes')||0), 1024*1024); res.setHeader('server-timing','app;dur=0.2, cfSpeedApp;dur=0.2'); res.end(Buffer.alloc(n)); return; }
  if (u.pathname === '/__up') { let n=0; req.on('data',d=>n+=d.length); req.on('end',()=>{res.setHeader('content-type','application/json');res.end(JSON.stringify({ok:true,acceptedBytes:n,serverDurationNs:1000000}));}); return; }
  let rel = decodeURIComponent(u.pathname).replace(/^\/+/, '') || 'index.html';
  const p = path.resolve(webRoot, rel);
  if (!p.startsWith(webRoot) || !fs.existsSync(p) || fs.statSync(p).isDirectory()) { res.statusCode=404;res.end('not found');return; }
  res.setHeader('content-type', mime[path.extname(p)]||'application/octet-stream'); fs.createReadStream(p).pipe(res);
});
await new Promise(r=>server.listen(0,'127.0.0.1',r));
const port=server.address().port;
const executableCandidates=['/usr/bin/chromium','/usr/bin/chromium-browser','/usr/bin/google-chrome','/usr/local/bin/chromium'];
const executablePath=executableCandidates.find(fs.existsSync);
const browser=await playwright.chromium.launch({headless:true,...(executablePath?{executablePath}:{}),args:['--no-sandbox','--disable-dev-shm-usage']});
const outDir=path.join(webRoot,'screenshots');fs.mkdirSync(outDir,{recursive:true});
const pages=[['index.html','standard.png'],['alternate.html','observatory.png'],['phosphor.html','phosphor.png']];
for (const [html,png] of pages) {
  const page=await browser.newPage({viewport:{width:1600,height:1000},deviceScaleFactor:1});
  await page.goto(`http://127.0.0.1:${port}/${html}`,{waitUntil:'networkidle'}).catch(()=>{});
  await page.evaluate(()=>{
    const values = [
      [['download','speed'], '486.7'], [['upload','speed'], '92.4'], [['latency'], '12.8'],
      [['jitter'], '2.1'], [['packet','loss'], '0.2'], [['loaded','download'], '28.4'],
      [['download','loaded'], '28.4'], [['loaded','upload'], '41.7'], [['upload','loaded'], '41.7'],
      [['bufferbloat'], 'A'], [['quality'], 'EXCELLENT'], [['confidence'], 'HIGH'],
      [['rtt'], '14.2'], [['asn'], 'AS64512'], [['provider'], 'NETSPEED'], [['server'], 'CASCADE EDGE 01']
    ];
    for (const el of document.querySelectorAll('[id], [class]')) {
      const key=((el.id||'')+' '+(typeof el.className==='string'?el.className:'')).toLowerCase();
      if (!/(value|result|number|metric|speed|latency|jitter|loss|grade|confidence|rtt|asn|provider|server)/.test(key)) continue;
      for (const [parts,val] of values) { if (parts.every(p=>key.includes(p))) { if (!el.querySelector('canvas,svg')) el.textContent=val; break; } }
    }
    for (const el of document.querySelectorAll('[hidden], .hidden, [aria-hidden="true"]')) { if (!el.classList.contains('apple-crt')) { el.hidden=false;el.classList.remove('hidden');if(el.getAttribute('aria-hidden')==='true')el.setAttribute('aria-hidden','false'); } }
    for (const el of document.querySelectorAll('[class*="progress"] > *, progress')) { if ('value' in el) el.value=100; el.style.width='100%'; }
    document.body.classList.add('test-complete','results-ready','demo-complete');
    const status=document.querySelector('[id*="status"], [class*="status"]'); if(status)status.textContent='TEST COMPLETE // ALL SYSTEMS NOMINAL';
    for(const c of document.querySelectorAll('canvas')) { const ctx=c.getContext('2d'); if(!ctx)continue; c.width=Math.max(c.clientWidth,400);c.height=Math.max(c.clientHeight,140);ctx.clearRect(0,0,c.width,c.height);ctx.strokeStyle='#33ff66';ctx.lineWidth=2;ctx.beginPath();for(let x=0;x<c.width;x+=8){const y=c.height*.58-Math.sin(x/37)*18-Math.sin(x/13)*5;if(x===0)ctx.moveTo(x,y);else ctx.lineTo(x,y);}ctx.stroke(); }
  });
  await page.addStyleTag({content:'*{animation:none!important;transition:none!important}'});
  await page.waitForTimeout(300);
  await page.screenshot({path:path.join(outDir,png),type:'png',fullPage:false});
  await page.close();
}
await browser.close();server.close();
