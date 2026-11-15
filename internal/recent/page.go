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
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>文件模式 · 最近请求</title>
<style>
body{font:14px/1.5 -apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC","Microsoft YaHei",sans-serif;margin:16px;color:#222;background:#fafafa}
h1{font-size:18px;margin:0 0 8px}.muted{color:#888;font-size:12px}
.bar{display:flex;gap:8px;align-items:center;flex-wrap:wrap;margin:8px 0 12px}
input{padding:5px 8px;border:1px solid #ccc;border-radius:6px;min-width:220px}button{padding:5px 12px;border:1px solid #bbb;border-radius:6px;background:#fff;cursor:pointer}
table{border-collapse:collapse;width:100%;background:#fff}th,td{border-bottom:1px solid #eee;padding:6px 8px;text-align:left;vertical-align:top}th{font-weight:600;background:#f3f3f3;white-space:nowrap}
.b{display:inline-block;padding:0 6px;border-radius:4px;font-size:12px;white-space:nowrap}.ok{background:#e3f6e8;color:#1a7f37}.warn{background:#fff4d6;color:#9a6700}.bad{background:#ffe3e3;color:#c62828}.dim{background:#eee;color:#666}
.mono{font-family:ui-monospace,Menlo,Consolas,monospace;font-size:12px}.err{color:#c62828;font-size:12px;word-break:break-all}
</style></head><body>
<h1>文件模式 · 最近请求</h1>
<div class="muted">只在内存里保留最近几条（重启 CPA 后清空），不记录请求和回复的正文。</div>
<div class="bar"><input id="key" type="password" placeholder="管理密钥（只存在这个浏览器里）"><button id="load">刷新</button><label><input id="auto" type="checkbox"> 每 5 秒自动刷新</label><span id="msg" class="muted"></span></div>
<table><thead><tr><th>时间</th><th>模型</th><th>客户端</th><th>结论</th><th>入站处理</th><th>回复</th><th>耗时</th><th>编号</th></tr></thead><tbody id="rows"></tbody></table>
<script>
const API=__API__;const $=(id)=>document.getElementById(id);
const esc=(s)=>String(s??'').replace(/[&<>"']/g,(c)=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'})[c]);
const b=(cls,t)=>'<span class="b '+cls+'">'+esc(t)+'</span>';
$('key').value=localStorage.getItem('filemode.managementKey')||'';
function inbound(r){if(r.inbound==='rewritten')return b('ok','正常')+' 改写成 '+r.files+' 个文件'+(r.reads?'，模型又读了 '+r.reads+' 次':'');
 if(r.inbound==='fallback')return b('warn','被上游拒绝')+' 已退回原请求'+(r.note?'<div class="err">'+esc(r.note)+'</div>':'');
 return b('dim','未改写')+' '+esc(r.note||'');}
function reply(r){switch(r.reply){case 'tool':return b('ok','正常')+' 回复工具已还原成文字';case 'plain':return b('warn','直接回文字')+' 没走回复工具，原样转发';
 case 'pass':return b('dim','原样转发');case 'pending':return b('dim','进行中');
 default:return b('bad','失败')+(r.status?' HTTP '+r.status:'')+(r.error?'<div class="err">'+esc(r.error)+'</div>':'');}}
function verdict(r){if(r.reply==='error')return b('bad','失败');if(r.reply==='pending')return b('dim','进行中');
 if(r.inbound==='rewritten'&&r.reply==='tool')return b('ok','正常');if(r.inbound==='skipped')return b('dim','未生效');return b('warn','注意');}
async function load(){const key=$('key').value.trim();localStorage.setItem('filemode.managementKey',key);$('msg').textContent='加载中…';
 try{const res=await fetch(API,{headers:key?{Authorization:'Bearer '+key}:{}});
  if(res.status===401||res.status===403){$('msg').textContent='管理密钥不对或没填';return;}
  if(!res.ok){$('msg').textContent='加载失败：HTTP '+res.status;return;}
  const j=await res.json();const list=j.records||[];
  $('rows').innerHTML=list.length?list.map((r)=>'<tr><td class="mono">'+esc(new Date(r.at).toLocaleString())+'</td><td class="mono">'+esc(r.model)+(r.stream?' <span class="muted">流式</span>':'')+'<div class="muted">'+esc(r.format)+'</div></td><td class="muted">'+esc(r.client||'-')+'</td><td>'+verdict(r)+'</td><td>'+inbound(r)+'</td><td>'+reply(r)+'</td><td class="mono">'+(r.reply==='pending'?'-':(r.duration_ms/1000).toFixed(1)+' s')+'</td><td class="mono muted">'+esc(r.id)+'</td></tr>').join(''):'<tr><td colspan="8" class="muted">还没有经过文件模式的请求。</td></tr>';
  $('msg').textContent='共 '+list.length+' 条（最多保留 '+j.size+' 条），'+new Date().toLocaleTimeString()+' 更新';
 }catch(e){$('msg').textContent='加载失败：'+e.message;}}
let timer=null;$('load').onclick=load;$('auto').onchange=()=>{clearInterval(timer);if($('auto').checked)timer=setInterval(load,5000);};
if($('key').value)load();
</script></body></html>`
