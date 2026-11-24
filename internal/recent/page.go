package recent

import (
	"encoding/json"
	"strings"
)

// Page returns the management page. It holds no data: the browser asks for the
// records at apiPath with the management key (resource pages are not
// authenticated by CPA, the management API is), which it keeps in localStorage.
func Page(apiPath string) []byte {
	p, _ := json.Marshal(apiPath)
	return []byte(strings.Replace(pageHTML, "__API__", string(p), 1))
}

const pageHTML = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover">
<meta name="color-scheme" content="light dark">
<title>文件模式 · 最近请求</title>
<style>
:root{--bg:#f5f6f8;--card:#fff;--fg:#1f2328;--mute:#6b7280;--line:#e6e8eb;--soft:#f0f2f4;--acc:#2563eb;
--ok:#16a34a;--ok-bg:#e8f7ee;--warn:#b45309;--warn-bg:#fef3e2;--bad:#dc2626;--bad-bg:#fdecec;--dim:#6b7280;--dim-bg:#eef0f2;--info:#2563eb;--info-bg:#e8effd;
--r:12px;--sh:0 1px 2px rgba(16,24,40,.06),0 1px 3px rgba(16,24,40,.08)}
@media (prefers-color-scheme:dark){:root{--bg:#0f1115;--card:#171a20;--fg:#e6e8eb;--mute:#9aa3ad;--line:#262b33;--soft:#1e222a;--acc:#60a5fa;
--ok:#4ade80;--ok-bg:#12301f;--warn:#fbbf24;--warn-bg:#33270e;--bad:#f87171;--bad-bg:#3a1717;--dim:#9aa3ad;--dim-bg:#242a32;--info:#60a5fa;--info-bg:#15233b;--sh:none}.btn.primary{color:#0b1220}}
*{box-sizing:border-box}html{-webkit-text-size-adjust:100%}
body{margin:0;background:var(--bg);color:var(--fg);font:14px/1.5 -apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC","Hiragino Sans GB","Microsoft YaHei",sans-serif;
padding:max(16px,env(safe-area-inset-top)) max(16px,env(safe-area-inset-right)) max(24px,env(safe-area-inset-bottom)) max(16px,env(safe-area-inset-left))}
.wrap{max-width:1080px;margin:0 auto}
.mono{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace}
.mute{color:var(--mute)}
header{display:flex;align-items:flex-start;justify-content:space-between;gap:12px;margin-bottom:14px}
h1{font-size:20px;line-height:1.3;margin:0;font-weight:650;letter-spacing:.2px}
.sub{font-size:12px;color:var(--mute);margin-top:4px}
.actions{display:flex;align-items:center;gap:8px;flex-shrink:0}
.btn{appearance:none;border:1px solid var(--line);background:var(--card);color:var(--fg);border-radius:10px;height:36px;padding:0 12px;font:inherit;cursor:pointer;display:inline-flex;align-items:center;gap:6px;box-shadow:var(--sh)}
.btn:active{transform:translateY(1px)}.btn.primary{background:var(--acc);border-color:var(--acc);color:#fff}
.btn.icon{width:36px;padding:0;justify-content:center}
.btn svg{width:16px;height:16px}.spin svg{animation:sp 1s linear infinite}@keyframes sp{to{transform:rotate(360deg)}}
.switch{display:inline-flex;align-items:center;gap:8px;font-size:13px;color:var(--mute);cursor:pointer;user-select:none}
.switch input{appearance:none;width:36px;height:22px;border-radius:11px;background:var(--line);position:relative;margin:0;cursor:pointer;transition:background .2s}
.switch input::after{content:"";position:absolute;top:3px;left:3px;width:16px;height:16px;border-radius:50%;background:#fff;transition:left .2s;box-shadow:0 1px 2px rgba(0,0,0,.2)}
.switch input:checked{background:var(--acc)}.switch input:checked::after{left:17px}
.card{background:var(--card);border:1px solid var(--line);border-radius:var(--r);box-shadow:var(--sh)}
.keybox{padding:14px;margin-bottom:14px}
.keybox .row{display:flex;gap:8px}
.keybox input{flex:1;min-width:0;height:40px;border:1px solid var(--line);border-radius:10px;padding:0 12px;font:inherit;background:var(--soft);color:var(--fg);outline:none}
.keybox input:focus{border-color:var(--acc)}
.keybox .hint{font-size:12px;color:var(--mute);margin-top:8px}
.keyline{display:flex;align-items:center;justify-content:space-between;gap:8px;font-size:12px;color:var(--mute);margin:-4px 0 12px}
.link{background:none;border:0;color:var(--acc);font:inherit;cursor:pointer;padding:0}
.msg{font-size:13px;margin:0 0 12px;padding:10px 12px;border-radius:10px;background:var(--bad-bg);color:var(--bad)}
.stats{display:grid;grid-template-columns:repeat(5,1fr);gap:10px;margin-bottom:14px}
.stat{padding:12px 14px;cursor:pointer;text-align:left;font:inherit;color:inherit;transition:border-color .15s}
.stat .n{font-size:22px;font-weight:650;line-height:1.2;font-variant-numeric:tabular-nums}
.stat .l{font-size:12px;color:var(--mute);display:flex;align-items:center;gap:6px}
.stat.on{border-color:var(--acc);box-shadow:0 0 0 1px var(--acc) inset}
.dot{width:8px;height:8px;border-radius:50%;display:inline-block;flex-shrink:0}
.d-ok{background:var(--ok)}.d-warn{background:var(--warn)}.d-bad{background:var(--bad)}.d-dim{background:var(--dim)}.d-info{background:var(--info)}
.toolbar{display:flex;align-items:center;gap:10px;margin-bottom:10px;flex-wrap:wrap}
.search{flex:1;min-width:160px;height:36px;border:1px solid var(--line);border-radius:10px;padding:0 12px;font:inherit;background:var(--card);color:var(--fg);outline:none}
.search:focus{border-color:var(--acc)}
.list{overflow:hidden}
.head,.item{display:grid;grid-template-columns:84px minmax(150px,1.3fr) 2fr 2fr 72px;gap:12px;align-items:center;padding:10px 16px}
.head{font-size:12px;color:var(--mute);background:var(--soft);border-bottom:1px solid var(--line)}
.item{border-bottom:1px solid var(--line);cursor:pointer;-webkit-tap-highlight-color:transparent}
.item:last-child{border-bottom:0}.item:hover{background:var(--soft)}
.item .when{font-size:12px;color:var(--mute);font-variant-numeric:tabular-nums}.item .when span{display:block}
.model{font-weight:600;word-break:break-all}.model small{font-weight:400;color:var(--mute);margin-left:6px;font-size:11px}
.tag{display:inline-flex;align-items:center;gap:5px;font-size:12px;padding:2px 8px;border-radius:999px;white-space:nowrap;font-weight:550}
.t-ok{background:var(--ok-bg);color:var(--ok)}.t-warn{background:var(--warn-bg);color:var(--warn)}.t-bad{background:var(--bad-bg);color:var(--bad)}.t-dim{background:var(--dim-bg);color:var(--dim)}.t-info{background:var(--info-bg);color:var(--info)}
.cell{min-width:0}.cell .d{font-size:12px;color:var(--mute);margin-top:2px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.dur{text-align:right;font-variant-numeric:tabular-nums;font-size:13px}
.detail{display:none;grid-column:1/-1;background:var(--soft);border-radius:10px;padding:10px 12px;font-size:12px;cursor:default}
.item.open .detail{display:block}.item.open .cell .d{white-space:normal}
.kv{display:grid;grid-template-columns:72px 1fr;gap:4px 10px}.kv div:nth-child(odd){color:var(--mute)}.kv div{word-break:break-all}
.err{color:var(--bad)}
.lbl{display:none}
.empty{padding:40px 16px;text-align:center;color:var(--mute)}
.skel{height:14px;border-radius:6px;background:linear-gradient(90deg,var(--soft),var(--line),var(--soft));background-size:200% 100%;animation:sk 1.2s infinite}@keyframes sk{to{background-position:-200% 0}}
footer{font-size:12px;color:var(--mute);text-align:center;margin-top:14px}
@media (max-width:760px){
 body{font-size:15px}
 header{flex-direction:column;align-items:stretch}.actions{justify-content:space-between}
 h1{font-size:18px}
 .stats{grid-template-columns:repeat(4,1fr);gap:8px}.stat{padding:10px}.stat .n{font-size:20px}.stat .l{font-size:11px;gap:4px}.stat.avg{display:none}
 .head{display:none}
 .list{background:none;border:0;box-shadow:none;display:flex;flex-direction:column;gap:10px}
 .item{grid-template-columns:1fr auto;grid-template-areas:"model when" "inb inb" "rep rep" "detail detail";gap:6px 10px;padding:12px 14px;background:var(--card);border:1px solid var(--line);border-radius:var(--r);box-shadow:var(--sh)}
 .item:last-child{border-bottom:1px solid var(--line)}.item:hover{background:var(--card)}
 .item .when{grid-area:when;text-align:right}.item .m{grid-area:model}.item .inb{grid-area:inb}.item .rep{grid-area:rep}.item .dur{display:none}.detail{grid-area:detail;margin-top:4px}
 .lbl{display:inline;font-size:12px;color:var(--mute);margin-right:6px}
 .cell .d{white-space:normal}
}
@media (max-width:340px){.stats{grid-template-columns:repeat(2,1fr)}}
</style></head><body><div class="wrap">
<header>
 <div><h1>文件模式 · 最近请求</h1><div class="sub">只在内存里保留最近几条，重启 CPA 后清空；不记录请求和回复的正文。</div></div>
 <div class="actions">
  <label class="switch"><input id="auto" type="checkbox">自动刷新</label>
  <button id="load" class="btn primary" type="button"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 12a9 9 0 1 1-2.64-6.36"/><path d="M21 3v6h-6"/></svg><span>刷新</span></button>
 </div>
</header>
<div id="keybox" class="card keybox" hidden>
 <div class="row"><input id="key" type="password" autocomplete="off" placeholder="CPA 管理密钥"><button id="save" class="btn primary" type="button">连接</button></div>
 <div class="hint">密钥只保存在这个浏览器里，用来调用 CPA 的管理接口。</div>
</div>
<div id="keyline" class="keyline" hidden><span>管理密钥已保存在本机</span><button id="change" class="link" type="button">更换</button></div>
<div id="msg" class="msg" hidden></div>
<div id="stats" class="stats"></div>
<div class="toolbar"><input id="q" class="search" type="search" placeholder="按模型或客户端筛选"><span id="count" class="mute" style="font-size:12px" hidden></span></div>
<div id="list" class="card list"></div>
<footer id="foot"></footer>
</div>
<script>
const API=__API__;const $=(id)=>document.getElementById(id);const KEY='filemode.managementKey';
const esc=(s)=>String(s??'').replace(/[&<>"']/g,(c)=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'})[c]);
const tag=(k,t)=>'<span class="tag t-'+k+'">'+esc(t)+'</span>';
let records=[],size=0,filter='all',open=new Set(),timer=null,busy=false;
const V={ok:['ok','正常'],warn:['warn','注意'],bad:['bad','失败'],dim:['dim','未生效'],run:['info','进行中']};
function verdict(r){if(r.reply==='error'||r.reply==='read_left')return'bad';if(r.reply==='pending')return'run';
 if(r.inbound==='rewritten'&&(r.reply_tool?r.reply==='tool':(r.reply==='text'||r.reply==='plain')))return'ok';if(r.inbound==='skipped')return'dim';return'warn';}
function inbound(r){switch(r.inbound){
 case'rewritten':return['改写成 '+r.files+' 个文件',(r.reads?'模型又读了 '+r.reads+' 次':'消息走读取工具送达')+(r.forced?'；读满后设 tool_choice 让模型作答':'')+(r.tool_choice_none?(r.reply_tool?'；tool_choice none 改为 auto':'；tool_choice none'):'')];
 
 case'fallback':return['被上游拒绝，已退回原请求',r.note||''];
 default:return['未改写',r.note||''];}}
function reply(r){switch(r.reply){
 case'tool':return['回复工具已还原成文字',''];case'read_left':return['读满后模型仍只要读取','这次读取调用已丢弃，客户端没收到答复'];case'plain':return r.reply_tool?['模型直接回了文字','没走回复工具，原样转发']:['文字回复','这次回复工具是关的'];case'text':return['文字回复','这次回复工具是关的'];
 case'pass':return['原样转发',''];case'pending':return['等待上游…',''];
 default:return['出错'+(r.status?' · HTTP '+r.status:''),r.error||''];}}
function ago(t){const s=(Date.now()-new Date(t))/1000;if(s<60)return'刚刚';if(s<3600)return Math.floor(s/60)+' 分钟前';if(s<86400)return Math.floor(s/3600)+' 小时前';return new Date(t).toLocaleDateString();}
const hm=(t)=>new Date(t).toLocaleTimeString('zh-CN',{hour12:false,hour:'2-digit',minute:'2-digit',second:'2-digit'});
const dur=(r)=>r.reply==='pending'?'—':(r.duration_ms/1000).toFixed(1)+' s';
function stats(){const c={ok:0,warn:0,bad:0,dim:0};let sum=0,n=0;
 records.forEach((r)=>{const v=verdict(r);if(c[v]!==undefined)c[v]++;if(r.reply!=='pending'){sum+=r.duration_ms;n++;}});
 const tile=(k,label,num,dot)=>'<button type="button" class="card stat'+(filter===k?' on':'')+'" data-f="'+k+'"><div class="n">'+num+'</div><div class="l"><span class="dot d-'+dot+'"></span>'+label+'</div></button>';
 $('stats').innerHTML=tile('ok','正常',c.ok,'ok')+tile('warn','注意',c.warn,'warn')+tile('bad','失败',c.bad,'bad')+tile('dim','未生效',c.dim,'dim')
  +'<div class="card stat avg" style="cursor:default"><div class="n">'+(n?(sum/n/1000).toFixed(1):'—')+'<span class="mute" style="font-size:13px;font-weight:400"> s</span></div><div class="l">平均耗时</div></div>';
 $('stats').querySelectorAll('[data-f]').forEach((b)=>b.onclick=()=>{filter=filter===b.dataset.f?'all':b.dataset.f;render();});}
function render(){stats();const q=$('q').value.trim().toLowerCase();
 const list=records.filter((r)=>(filter==='all'||verdict(r)===filter)&&(!q||(r.model+' '+(r.client||'')+' '+r.format).toLowerCase().includes(q)));
 const ct=(filter!=='all'||q)?list.length+' / '+records.length+' 条':'';$('count').textContent=ct;$('count').hidden=!ct;
 if(!records.length){$('list').innerHTML='<div class="empty">还没有经过文件模式的请求。<br>用带前缀的模型发一条消息试试。</div>';return;}
 if(!list.length){$('list').innerHTML='<div class="empty">没有符合条件的记录。</div>';return;}
 $('list').innerHTML='<div class="head"><div>时间</div><div>模型</div><div>入站</div><div>回复</div><div style="text-align:right">耗时</div></div>'+list.map((r)=>{
  const v=V[verdict(r)],i=inbound(r),p=reply(r),o=open.has(r.id);
  return '<div class="item'+(o?' open':'')+'" data-id="'+esc(r.id)+'">'
   +'<div class="when"><span>'+esc(hm(r.at))+'</span><span class="w2">'+esc(ago(r.at))+'</span></div>'
   +'<div class="cell m"><div class="model mono">'+esc(r.model)+'</div><div class="d">'+tag(v[0],v[1])+' <span class="mute">'+esc(r.format)+(r.stream?' · 流式':'')+'</span></div></div>'
   +'<div class="cell inb"><span class="lbl">入站</span>'+esc(i[0])+(i[1]?'<div class="d'+(r.inbound==='fallback'?' err':'')+'">'+esc(i[1])+'</div>':'')+'</div>'
   +'<div class="cell rep"><span class="lbl">回复</span>'+esc(p[0])+(p[1]?'<div class="d'+(r.reply==='error'?' err':'')+'">'+esc(p[1])+'</div>':'')+'</div>'
   +'<div class="dur mono">'+dur(r)+'</div>'
   +'<div class="detail"><div class="kv"><div>编号</div><div class="mono">'+esc(r.id)+'</div><div>时间</div><div>'+esc(new Date(r.at).toLocaleString())+'</div><div>耗时</div><div>'+dur(r)+'</div><div>格式</div><div>'+esc(r.format)+(r.stream?' · 流式':' · 非流式')+'</div><div>客户端</div><div>'+esc(r.client||'—')+'</div><div>回复工具</div><div>'+(r.reply_tool?'开':'关')+'</div>'
   +(r.note?'<div>说明</div><div>'+esc(r.note)+'</div>':'')+(r.error?'<div>错误</div><div class="err">'+esc(r.error)+'</div>':'')+'</div></div></div>';}).join('');
 $('list').querySelectorAll('.item').forEach((el)=>el.onclick=(e)=>{if(e.target.closest('.detail'))return;const id=el.dataset.id;open.has(id)?open.delete(id):open.add(id);el.classList.toggle('open');});}
function showKey(on){$('keybox').hidden=!on;$('keyline').hidden=on||!localStorage.getItem(KEY);if(on)setTimeout(()=>$('key').focus(),0);}
function msg(t){$('msg').hidden=!t;$('msg').textContent=t||'';}
async function load(){const key=localStorage.getItem(KEY)||'';if(!key){showKey(true);return;}if(busy)return;busy=true;$('load').classList.add('spin');
 if(!records.length&&!$('list').dataset.done)$('list').innerHTML='<div style="padding:16px;display:grid;gap:12px"><div class="skel"></div><div class="skel" style="width:70%"></div><div class="skel" style="width:85%"></div></div>';
 try{const res=await fetch(API,{headers:{Authorization:'Bearer '+key},cache:'no-store'});
  if(res.status===401||res.status===403){msg('管理密钥不对，请重新填写。');showKey(true);return;}
  if(!res.ok){msg('加载失败：HTTP '+res.status);return;}
  const j=await res.json();records=j.records||[];size=j.size||0;msg('');showKey(false);$('list').dataset.done=1;render();
  $('foot').textContent='共 '+records.length+' 条，最多保留 '+size+' 条 · '+new Date().toLocaleTimeString()+' 更新';
 }catch(e){msg('加载失败：'+e.message);}finally{busy=false;$('load').classList.remove('spin');}}
$('save').onclick=()=>{const k=$('key').value.trim();if(!k){$('key').focus();return;}localStorage.setItem(KEY,k);$('key').value='';load();};
$('key').onkeydown=(e)=>{if(e.key==='Enter')$('save').click();};
$('change').onclick=()=>{$('key').value=localStorage.getItem(KEY)||'';showKey(true);};
$('load').onclick=load;$('q').oninput=render;
const AUTO='filemode.autoRefresh';$('auto').checked=localStorage.getItem(AUTO)==='1';
function arm(){clearInterval(timer);timer=null;if($('auto').checked&&!document.hidden)timer=setInterval(load,5000);}
$('auto').onchange=()=>{localStorage.setItem(AUTO,$('auto').checked?'1':'0');arm();if($('auto').checked)load();};
document.addEventListener('visibilitychange',()=>{arm();if(!document.hidden&&$('auto').checked)load();});
arm();load();
</script></body></html>`
