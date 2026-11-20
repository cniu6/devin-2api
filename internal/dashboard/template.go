package dashboard

const loginPage = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Devin·2API 控制台</title>
<style>
*{margin:0;padding:0;box-sizing:border-box}
:root{--bg:#090b10;--panel:#10141c;--panel2:#171c27;--line:#232a3a;--text:#dde2ee;--mut:#8a93a8;--acc:#7c8aff;--acc2:#22d3ee}
body{font-family:"Segoe UI",system-ui,-apple-system,"Microsoft YaHei",sans-serif;background:var(--bg);color:var(--text);display:flex;justify-content:center;align-items:center;min-height:100vh}
.login{background:var(--panel);border:1px solid var(--line);border-radius:14px;padding:34px 32px;width:340px;box-shadow:0 8px 40px rgba(0,0,0,.5)}
.mark{width:34px;height:34px;border-radius:9px;margin:0 auto 14px;background:linear-gradient(135deg,var(--acc),var(--acc2))}
.login h1{font-size:15px;font-weight:700;text-align:center;margin-bottom:4px}
.login .sub{font-size:11px;color:var(--mut);text-align:center;margin-bottom:20px}
.login input{width:100%;padding:10px 13px;border:1px solid var(--line);border-radius:8px;background:var(--bg);color:var(--text);font-size:13px;margin-bottom:12px;outline:none;transition:border-color .15s}
.login input:focus{border-color:var(--acc)}
.login button{width:100%;padding:10px;border:none;border-radius:8px;background:linear-gradient(135deg,var(--acc),#5f6ef0);color:#fff;font-size:13px;font-weight:600;cursor:pointer;transition:opacity .15s}
.login button:hover{opacity:.88}
.err{color:#f87171;font-size:12px;text-align:center;margin-top:10px;display:none}
</style>
</head>
<body>
<div class="login">
<div class="mark"></div>
<h1>Devin·2API 控制台</h1>
<div class="sub">请输入面板密码</div>
<form id="f">
<input type="password" id="p" placeholder="密码" autofocus>
<button type="submit">登录</button>
</form>
<div class="err" id="err">密码错误</div>
</div>
<script>
document.getElementById('f').addEventListener('submit',async e=>{
e.preventDefault();
const res=await fetch('/panel/login',{method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded'},body:'password='+encodeURIComponent(document.getElementById('p').value)});
if(res.ok){location.reload()}else{const el=document.getElementById('err');el.style.display='block';setTimeout(()=>el.style.display='none',2000)}
});
</script>
</body>
</html>`

// dashboardPage 是单文件前端：紧凑暗黑风格，含筛选、分类、促销与价格合并展示。
const dashboardPage = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Devin·2API 控制台</title>
<style>
*{margin:0;padding:0;box-sizing:border-box}
:root{
--bg:#090b10;--panel:#10141c;--panel2:#171c27;--line:#232a3a;
--text:#dde2ee;--mut:#8a93a8;--faint:#59637a;
--acc:#7c8aff;--acc2:#22d3ee;
--green:#4ade80;--blue:#60a5fa;--amber:#fbbf24;--red:#f87171;--purple:#c084fc;--cyan:#22d3ee;--pink:#f472b6;
}
body{font-family:"Segoe UI",system-ui,-apple-system,"Microsoft YaHei",sans-serif;background:var(--bg);color:var(--text);padding:14px 18px 36px;font-size:13px}
.wrap{max-width:1560px;margin:0 auto}
.topbar{display:flex;align-items:center;gap:10px;margin-bottom:12px}
.mark{width:22px;height:22px;border-radius:6px;background:linear-gradient(135deg,var(--acc),var(--acc2))}
.topbar h1{font-size:15px;font-weight:700;letter-spacing:.2px}
.topbar h1 b{color:var(--acc)}
.sub{font-size:11px;color:var(--faint)}
.grow{flex:1}
.btn{padding:5px 13px;border:1px solid var(--line);border-radius:7px;background:var(--panel2);color:var(--mut);font-size:12px;cursor:pointer;transition:.15s}
.btn:hover{border-color:var(--acc);color:var(--acc)}
.section{background:var(--panel);border:1px solid var(--line);border-radius:10px;padding:12px 14px;margin-bottom:10px}
.sec-t{font-size:11px;font-weight:700;color:var(--mut);letter-spacing:.8px;margin-bottom:10px;display:flex;align-items:center;gap:7px}
.sec-t::before{content:"";width:7px;height:7px;border-radius:2px;background:linear-gradient(135deg,var(--acc),var(--acc2))}
.grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(150px,1fr));gap:8px}
.card{background:var(--panel2);border:1px solid var(--line);border-radius:8px;padding:9px 11px}
.card .label{font-size:10px;color:var(--mut);margin-bottom:3px;letter-spacing:.3px}
.card .value{font-size:14px;font-weight:600;word-break:break-all}
.progress-bar{width:100%;height:6px;background:#262d3e;border-radius:3px;margin-top:6px;overflow:hidden}
.progress-fill{height:100%;border-radius:3px;transition:width .5s}
.kv-row{display:flex;flex-wrap:wrap;gap:4px 18px;margin-top:8px}
.kv{font-size:12px;line-height:1.7}
.kv .k{color:var(--mut);margin-right:4px}
.kv .v{color:var(--text);font-weight:600}
.ring-row{display:flex;gap:26px;flex-wrap:wrap;margin-top:10px}
.ring-wrap{display:flex;flex-direction:column;align-items:center;gap:3px}
.ring-wrap .ring-label{font-size:11px;color:var(--mut)}
.ring-wrap .ring-pct{font-size:14px;font-weight:700}
.ring-countdown{font-size:10px;color:var(--faint)}
.ring-svg{width:72px;height:72px;transform:rotate(-90deg)}
.ring-bg{fill:none;stroke:#262d3e;stroke-width:7}
.ring-fg{fill:none;stroke-width:7;stroke-linecap:round;transition:stroke-dashoffset .5s}
.filters{display:flex;flex-wrap:wrap;gap:6px;margin-bottom:8px;align-items:center}
.filters select,.filters input[type=search]{padding:6px 10px;border:1px solid var(--line);border-radius:7px;background:var(--panel2);color:var(--text);font-size:12px;outline:none}
.filters select:focus,.filters input[type=search]:focus{border-color:var(--acc)}
.filters input[type=search]{flex:1;min-width:160px}
.chk{display:inline-flex;align-items:center;gap:5px;font-size:12px;color:var(--mut);cursor:pointer;user-select:none;padding:4px 2px}
.chk input{accent-color:var(--acc);cursor:pointer}
.chip-row{display:flex;flex-wrap:wrap;gap:5px;margin-bottom:8px}
.chip{padding:3px 9px;border-radius:999px;border:1px solid var(--line);background:var(--panel2);color:var(--mut);font-size:11px;cursor:pointer;user-select:none;transition:.12s}
.chip:hover{border-color:var(--acc);color:var(--text)}
.chip.on{background:#7c8aff1f;border-color:var(--acc);color:#aab4ff}
.tabs{display:flex;gap:6px;margin-bottom:12px}
.tab{padding:6px 18px;border:1px solid var(--line);border-radius:8px;background:var(--panel);color:var(--mut);font-size:12px;font-weight:600;cursor:pointer;transition:.15s}
.tab:hover{color:var(--text);border-color:var(--acc)}
.tab.on{background:#7c8aff1f;border-color:var(--acc);color:#aab4ff}
.ring-box{position:relative;width:72px;height:72px}
.ring-center{position:absolute;inset:0;display:flex;flex-direction:column;align-items:center;justify-content:center}
.ring-num{font-size:11px;font-weight:700;line-height:1.2}
.ring-dim{font-size:9px;color:var(--faint);font-weight:400}
.gift{position:relative;display:inline-block;margin-left:4px;vertical-align:-2px;cursor:default}
.gift svg{width:13px;height:13px;display:block}
.gift .pop{display:none;position:absolute;left:50%;top:-8px;transform:translate(-50%,-100%);z-index:60;min-width:180px;background:#1a2030;border:1px solid #c084fc55;border-radius:8px;padding:9px 11px;font-size:11px;line-height:1.9;color:var(--text);box-shadow:0 8px 28px rgba(0,0,0,.6);white-space:nowrap}
.gift:hover .pop{display:block}
.gift .pop .pt{font-weight:700;color:#d8b4fe;margin-bottom:2px}
.gift .pop .pk{color:var(--mut);display:inline-block;min-width:34px}
.strike{color:var(--faint);text-decoration:line-through;text-decoration-color:#f87171}
.stat-cards{display:grid;grid-template-columns:repeat(auto-fill,minmax(148px,1fr));gap:8px;margin-bottom:12px}
.stat-cards .card .value{font-size:16px}
.stat-cards .sub{font-size:10px;color:var(--faint);margin-top:2px}
.chart-box{border:1px solid var(--line);border-radius:8px;background:var(--panel2);padding:10px 12px;margin-bottom:10px}
.chart-box canvas{width:100%;height:190px;display:block}
.chart-legend{display:flex;gap:14px;font-size:10px;color:var(--mut);margin-top:6px}
.chart-legend i{display:inline-block;width:9px;height:9px;border-radius:2px;margin-right:4px;vertical-align:-1px}
.result-ok{color:var(--green)}
.result-bad{color:var(--red)}
.result-warn{color:var(--amber)}
.rec-row{cursor:pointer}
.rec-row:hover td{background:#1a2133}
tr.detail>td{padding:0 9px 10px;background:#0d1119}
.detail-grid{display:grid;grid-template-columns:1fr 1.2fr 1.2fr;gap:8px;padding:8px 0}
.detail-h{font-size:10px;font-weight:700;color:var(--mut);letter-spacing:.5px;margin-bottom:4px}
.detail-grid pre{max-height:220px;overflow:auto;margin:0;background:var(--bg);border:1px solid var(--line);border-radius:6px;padding:8px;font-size:10px;line-height:1.55;color:#b8c2d8;white-space:pre-wrap;word-break:break-all}
.stats{font-size:11px;color:var(--mut);margin-bottom:7px}
.stats b{color:var(--text);font-weight:600}
.model-scroll{max-height:62vh;overflow:auto;border:1px solid var(--line);border-radius:8px}
.model-scroll::-webkit-scrollbar{width:8px;height:8px}
.model-scroll::-webkit-scrollbar-thumb{background:#2a3245;border-radius:4px}
.model-scroll::-webkit-scrollbar-track{background:transparent}
table{width:100%;border-collapse:collapse;font-size:12px}
th{text-align:left;padding:7px 9px;border-bottom:1px solid var(--line);color:var(--mut);font-size:10px;font-weight:700;letter-spacing:.5px;position:sticky;top:0;background:#141926;z-index:1;white-space:nowrap}
td{padding:6px 9px;border-bottom:1px solid #1a2030;vertical-align:top}
tbody tr:last-child td{border-bottom:none}
tbody tr:hover{background:#151b28}
tr.fam td{background:#12182a}
tr.fam:hover td{background:#171e30}
.mono{font-family:ui-monospace,Consolas,Menlo,monospace;font-size:11px}
.badge{display:inline-flex;align-items:center;padding:1px 7px;border-radius:5px;font-size:10px;font-weight:600;margin:1px 3px 1px 0;white-space:nowrap;border:1px solid transparent}
.badge-free{background:#4ade8014;border-color:#4ade8038;color:var(--green)}
.badge-low{background:#60a5fa14;border-color:#60a5fa38;color:var(--blue)}
.badge-medium{background:#fbbf2414;border-color:#fbbf2438;color:var(--amber)}
.badge-high{background:#f8717114;border-color:#f8717138;color:var(--red)}
.badge-promo{background:#c084fc14;border-color:#c084fc45;color:var(--purple)}
.badge-beta{background:#fde04714;border-color:#fde04738;color:#fde047}
.badge-fast{background:#22d3ee14;border-color:#22d3ee38;color:var(--cyan)}
.badge-img{background:#a78bfa14;border-color:#a78bfa38;color:#a78bfa}
.badge-new{background:#34d39914;border-color:#34d39938;color:#34d399}
.badge-premium{background:#f59e0b14;border-color:#f59e0b38;color:#f59e0b}
.badge-rec{background:#93c5fd14;border-color:#93c5fd38;color:#93c5fd}
.badge-cap{background:#f472b614;border-color:#f472b638;color:var(--pink)}
.badge-cmd{background:#fbbf2414;border-color:#fbbf2438;color:#fbbf24}
.badge-off{background:#262d3e;border-color:#333c52;color:var(--mut)}
.cat{display:inline-block;padding:0 6px;border-radius:4px;background:#1d2433;border:1px solid #2a3245;color:#9aa8c7;font-size:10px;line-height:16px;margin:0 2px 2px 0;white-space:nowrap}
.cat-fam{background:#16283a;border-color:#24506e;color:#7dd3fc}
.price{font-variant-numeric:tabular-nums;white-space:nowrap}
.price.ok{color:var(--green)}
.muted{color:var(--faint)}
.loading{text-align:center;padding:26px;color:var(--mut)}
.note{font-size:11px;color:var(--mut);line-height:1.6;margin-top:8px}
.note code{background:#1d2433;border:1px solid #2a3245;padding:0 5px;border-radius:4px;font-size:10px}
.err-line{color:var(--red);margin-top:8px;font-size:12px}
</style>
</head>
<body>
<div class="wrap">
<div class="topbar">
<div class="mark"></div>
<h1>Devin<b>·2API</b> 控制台</h1>
<span class="sub" id="upstream"></span>
<div class="grow"></div>
<button class="btn" onclick="refreshAll()">刷新</button>
</div>

<div class="tabs">
<span class="tab on" id="tabStats" onclick="showPage('stats')">统计</span>
<span class="tab" id="tabModels" onclick="showPage('models')">模型</span>
</div>

<div class="section" id="statusSection">
<div class="sec-t">账户 · 用量 · 容量</div>
<div class="loading">加载中...</div>
</div>

<div id="pageStats">
<div class="section">
<div class="sec-t">请求 · Token · 统计</div>
<div class="stat-cards" id="statCards"><div class="loading">加载中...</div></div>
<div class="chart-box"><canvas id="statChart"></canvas><div class="chart-legend"><span><i style="background:#5b8cff"></i>输入 tokens</span><span><i style="background:#22d3ee"></i>输出 tokens</span><span><i style="background:#fbbf24"></i>请求数</span></div></div>
<div class="sec-t" style="margin-top:10px">最近请求 · 20</div>
<div class="model-scroll" style="max-height:38vh">
<table id="recentTable">
<thead><tr><th>时间</th><th>IP</th><th>接口</th><th>模型</th><th>结果</th><th>耗时</th><th>输入</th><th>输出</th><th>缓存 读/写</th><th>合计</th></tr></thead>
<tbody><tr><td colspan="10" class="loading">暂无数据</td></tr></tbody>
</table>
</div>
<div class="note" style="margin-top:6px">点击行展开 请求头 / 请求体 / 响应；详情仅存内存，不落盘。</div>
<div class="note">统计为进程内存聚合，每 5 分钟落盘到 <code>stats.json</code>（配置文件同目录），退出时再保存一次；在线时长为本次启动起算。</div>
</div>
</div>

<div id="pageModels" style="display:none">
<div class="section">
<div class="sec-t">模型 · 分类 · 价格</div>
<div class="filters">
<input type="search" id="search" placeholder="搜索 UID / 标签 / 描述 / 系列 / 分类..." oninput="applyFilters()">
<select id="fGroup" onchange="applyFilters()"><option value="">全部分类</option></select>
<select id="fProvider" onchange="applyFilters()"><option value="">全部渠道</option></select>
<select id="fApi" onchange="applyFilters()"><option value="">全部 API Provider</option></select>
<select id="fTier" onchange="applyFilters()">
<option value="">全部定价等级</option>
<option value="free">FREE</option>
<option value="low">LOW</option>
<option value="medium">MEDIUM</option>
<option value="high">HIGH</option>
</select>
<select id="fPricing" onchange="applyFilters()"><option value="">全部计费类型</option></select>
<select id="fSort" onchange="applyFilters()">
<option value="default">默认排序</option>
<option value="mult_asc">倍率 升序</option>
<option value="mult_desc">倍率 降序</option>
<option value="in_asc">Input $/1M 升序</option>
<option value="in_desc">Input $/1M 降序</option>
<option value="out_asc">Output $/1M 升序</option>
<option value="out_desc">Output $/1M 降序</option>
<option value="final_asc">最终价格 升序</option>
<option value="final_desc">最终价格 降序</option>
<option value="name">名称 A-Z</option>
</select>
<label class="chk"><input type="checkbox" id="fMerge" onchange="applyFilters()">合并同系列</label>
</div>
<div class="chip-row" id="chips">
<span class="chip" data-tag="free" onclick="toggleChip(this)">FREE</span>
<span class="chip" data-tag="promo" onclick="toggleChip(this)">PROMO</span>
<span class="chip" data-tag="img" onclick="toggleChip(this)">支持图片</span>
<span class="chip" data-tag="beta" onclick="toggleChip(this)">BETA</span>
<span class="chip" data-tag="new" onclick="toggleChip(this)">NEW</span>
<span class="chip" data-tag="fast" onclick="toggleChip(this)">FAST</span>
<span class="chip" data-tag="premium" onclick="toggleChip(this)">Premium</span>
<span class="chip" data-tag="rec" onclick="toggleChip(this)">推荐</span>
<span class="chip" data-tag="empty_mult" onclick="toggleChip(this)">无倍率(FREE/基准)</span>
<span class="chip" data-tag="disabled" onclick="toggleChip(this)">已禁用</span>
</div>
<div class="stats" id="stats">加载中...</div>
<div class="model-scroll">
<table id="modelTable">
<thead>
<tr>
<th>#</th>
<th>模型</th>
<th>分类</th>
<th>渠道</th>
<th>等级</th>
<th>倍率</th>
<th>Input $/1M</th>
<th>Cached $/1M</th>
<th>Output $/1M</th>
<th>最终价格</th>
<th>计费</th>
<th>标签</th>
</tr>
</thead>
<tbody><tr><td colspan="12" class="loading">加载中...</td></tr></tbody>
</table>
</div>
<div class="note">
<strong>价格说明：</strong>
<code>倍率</code> 是每请求消耗 credit 数，FREE 为 0，未下发按基准 ≈1 计。
<code>Input / Cached / Output</code> 为上游 <code>model_dimensions</code> 下发的 $/1M tokens 参考价。
<code>最终价格</code> = 每请求倍率 + 进出价合并区间（含 effort 低~高浮动）。
<code>分类</code> 来自上游 picker 分组（client_model_sorts）与模型系列；促销模型名称旁的 🎁 悬浮显示优惠详情。
计费类型：STATIC_CREDIT=固定 credit、API=按 API、BYOK=自带 Key、ACU_*=ACU 计费。
</div>
</div>
</div>
</div>

<script>
let allModels=[];
let activeTags=new Set();

function esc(s){
return String(s==null?'':s)
.replace(/&/g,'&amp;')
.replace(/</g,'&lt;')
.replace(/>/g,'&gt;')
.replace(/"/g,'&quot;');
}
function card(label,value){
return '<div class="card"><div class="label">'+esc(label)+'</div><div class="value">'+esc(String(value))+'</div></div>';
}
// kv 生成一行键值对（无卡片背景，紧凑排列）
function kv(k,v){return '<span class="kv"><span class="k">'+esc(k)+'</span><span class="v">'+esc(String(v))+'</span></span>'}
// ring 生成环形进度图（SVG），percent 0-100，即使 0 也显示
// 环中心显示百分比与 剩余/总量 数字；resetUnix 为重置时间戳（秒），total 为估算总量
function ring(label,percent,resetUnix,total){
const p=Math.max(0,Math.min(100,Number(percent)||0));
const r=29,circ=2*Math.PI*r,off=circ*(1-p/100);
const color=p>50?'#4ade80':p>20?'#fbbf24':'#f87171';
let countdown='';
if(resetUnix&&Number(resetUnix)>0){
const diff=Number(resetUnix)*1000-Date.now();
if(diff>0){
const d=Math.floor(diff/86400000),h=Math.floor(diff%86400000/3600000),m=Math.floor(diff%3600000/60000);
if(d>0){countdown=d+'d '+h+'h '+m+'m'}else if(h>0){countdown=h+'h '+m+'m'}else{countdown=m+'m'}
}else{countdown='已重置'}
}
// 环内数值：有总量时显示 剩余/总量，配额制（百分比）回退显示 剩余%/100
let inside;
if(Number.isFinite(Number(total))&&Number(total)>0){
const rem=Math.round(p/100*Number(total));
inside='<div class="ring-num">'+rem+'<span class="ring-dim">/'+total+'</span></div>';
}else{
inside='<div class="ring-num">'+Math.round(p)+'<span class="ring-dim">/100</span></div>';
}
return '<div class="ring-wrap"><div class="ring-box"><svg class="ring-svg" viewBox="0 0 72 72"><circle class="ring-bg" cx="36" cy="36" r="'+r+'"/><circle class="ring-fg" cx="36" cy="36" r="'+r+'" stroke="'+color+'" stroke-dasharray="'+circ.toFixed(1)+'" stroke-dashoffset="'+off.toFixed(1)+'"/></svg><div class="ring-center"><div class="ring-pct" style="color:'+color+';font-size:13px">'+Math.round(p)+'%</div>'+inside+'</div></div><div class="ring-label">'+esc(label)+'</div>'+(countdown?'<div class="ring-countdown">'+countdown+'</div>':'')+'</div>';
}

function fmtQuota(v){
if(v==null||v===''||v===undefined) return '-';
if(Number(v)===-1) return '不限 / 配额制';
return String(v);
}
function fmtUnix(v){
if(v==null||v===''||Number(v)===0) return '-';
const n=Number(v);
if(!Number.isFinite(n)||n<=0) return String(v);
try{return new Date(n*1000).toLocaleString()}catch(e){return String(v)}
}
function bar(label,percent){
const p=Number(percent);
if(!Number.isFinite(p)) return '';
if(p<=0){
return '<div style="margin-top:10px"><div class="label" style="font-size:11px;color:var(--mut);margin-bottom:3px">'+esc(label)+': 已用尽</div><div class="progress-bar"><div class="progress-fill" style="width:100%;background:#f87171"></div></div></div>';
}
const color=p>50?'#4ade80':p>20?'#fbbf24':'#f87171';
return '<div style="margin-top:10px"><div class="label" style="font-size:11px;color:var(--mut);margin-bottom:3px">'+esc(label)+': '+p+'%</div><div class="progress-bar"><div class="progress-fill" style="width:'+Math.min(100,p)+'%;background:'+color+'"></div></div></div>';
}

async function loadStatus(){
const res=await fetch('/panel/api/status');
const data=await res.json();
if(data.upstream){document.getElementById('upstream').textContent='上游 '+String(data.upstream).replace(/^https?:\/\//,'')}
const el=document.getElementById('statusSection');
let html='<div class="sec-t">账户 · 用量 · 容量</div>';
if(data.user){
const u=data.user;
html+='<div class="kv-row">';
html+=kv('用户名',u.name||'-');
html+=kv('邮箱',u.email||'-');
html+=kv('Pro',u.pro?'是':'否');
html+=kv('Tier',u.teams_tier||'-');
html+=kv('User ID',u.user_id||'-');
html+='</div>';
}
if(data.plan_status||data.plan_info){
const ps=data.plan_status||{};
const pi=data.plan_info||{};
html+='<div class="sec-t" style="margin-top:14px">套餐与配额</div>';
// 环形图：日/周配额剩余（始终显示，即使 0%）
html+='<div class="ring-row">';
const total=ps.monthly_prompt_credits??pi.monthly_prompt_credits;
html+=ring('日配额剩余',ps.daily_quota_remaining!=null?ps.daily_quota_remaining:0,ps.daily_quota_reset,total);
html+=ring('周配额剩余',ps.weekly_quota_remaining!=null?ps.weekly_quota_remaining:0,ps.weekly_quota_reset,total);
html+='</div>';
// 紧凑键值对
html+='<div class="kv-row">';
html+=kv('套餐',ps.plan_name||pi.plan_name||'-');
html+=kv('计费',ps.billing_strategy||pi.billing_strategy||'-');
html+=kv('月 Prompt',fmtQuota(ps.monthly_prompt_credits??pi.monthly_prompt_credits));
html+=kv('可用 Prompt',fmtQuota(ps.available_prompt_credits));
html+=kv('可用 Flow',fmtQuota(ps.available_flow_credits));
html+=kv('可用 Flex',fmtQuota(ps.available_flex_credits));
html+=kv('周期',(ps.plan_start||'-')+' ~ '+(ps.plan_end||'-'));
html+=kv('超额',ps.overage_balance_micros??'-');
html+=kv('ACU',(ps.acu_consumed??'-')+' / '+(ps.acu_limit??'-'));
html+='</div>';
html+='<div class="note" style="margin-top:8px">Pro 多为 <strong>配额制 (QUOTA)</strong>：优先看日/周剩余百分比。月 Prompt 为 -1 表示不按固定 monthly credit 计。</div>';
}
if(data.capacity){
html+='<div class="kv-row" style="margin-top:8px">';
html+=kv('有容量',data.capacity.has_capacity?'是':'否');
html+=kv('活跃会话',data.capacity.active_sessions??'-');
html+=kv('容量消息',data.capacity.message||'-');
html+='</div>';
}
if(data.ide_status){
html+='<div class="kv-row" style="margin-top:4px">';
html+=kv('IDE 状态',data.ide_status.level||'-');
html+=kv('IDE 消息',data.ide_status.message||'-');
html+='</div>';
}
if(data.model_statuses&&data.model_statuses.length){
html+='<div class="sec-t" style="margin-top:14px">模型状态告警</div><div class="grid">';
data.model_statuses.forEach(s=>{html+=card(s.model||'-',s.status||'-')});
html+='</div>';
}
if(!data.user && data.user_status_error){
html+='<div class="err-line">账户用量拉取失败: '+esc(data.user_status_error)+'</div>';
} else if(!data.user){
html+='<div class="note" style="margin-top:10px">暂无账户用量数据。</div>';
}
if(data.capacity_error){html+='<div class="err-line">'+esc(data.capacity_error)+'</div>'}
el.innerHTML=html;
}

function toggleChip(el){
const tag=el.dataset.tag;
if(activeTags.has(tag)){activeTags.delete(tag);el.classList.remove('on')}
else{activeTags.add(tag);el.classList.add('on')}
applyFilters();
}

function fillSelect(id,values){
const el=document.getElementById(id);
const cur=el.value;
const keep=el.options[0].outerHTML;
const opts=[...values].filter(Boolean).sort().map(v=>'<option value="'+esc(v)+'">'+esc(v)+'</option>').join('');
el.innerHTML=keep+opts;
if([...el.options].some(o=>o.value===cur)) el.value=cur;
}

// fmtP 紧凑格式化单价：去尾零
function fmtP(v){
const n=Number(v);
if(!Number.isFinite(n))return'-';
const s=n>=100?n.toFixed(1):n>=1?n.toFixed(2):n.toFixed(3);
return s.replace(/\.?0+$/,'')||'0';
}
function trimN(v){
const n=Number(v);
return n%1?n.toFixed(1):String(n);
}

// dimOf 按标签取出价格维度（input / cached / output）
function dimOf(m,kind){
const names={input:['input'],cached:['cached input','cached_input','cache read','cache_read'],output:['output']}[kind]||[];
for(const d of (m.dimensions||[])){
if(names.indexOf(String(d.label).toLowerCase())>=0) return d;
}
return null;
}
// rng 维度有效区间：min/max 存在时构成低~高区间
function rng(d){
if(!d) return null;
const v=Number(d.value);
const lo=(d.min!=null&&Number(d.min)<v)?Number(d.min):v;
const hi=(d.max!=null&&Number(d.max)>v)?Number(d.max):v;
return {lo:lo,hi:hi};
}
// prOf 计算模型的价格区间（优先维度 min/max，缺失回退到单值字段）
function prOf(m){
const i=rng(dimOf(m,'input')),c=rng(dimOf(m,'cached')),o=rng(dimOf(m,'output'));
return {
i:i||(m.price_input!=null?{lo:+m.price_input,hi:+m.price_input}:null),
c:c||(m.price_cached!=null?{lo:+m.price_cached,hi:+m.price_cached}:null),
o:o||(m.price_output!=null?{lo:+m.price_output,hi:+m.price_output}:null)
};
}
function fmtR(r){return r?(r.lo===r.hi?('$'+fmtP(r.lo)):('$'+fmtP(r.lo)+'~'+fmtP(r.hi))):'—'}

// finalPrice 合成"最终价格"：每请求 credit 倍率 + 进出价区间
function finalPrice(m){
if(/BYOK/.test(m.pricing_type||'')) return '<span class="price">BYOK</span>';
if(m.cost_tier==='free'&&(!m.credit_multiplier||m.credit_multiplier===0)) return '<span class="price ok">免费</span>';
const pr=m._pr||prOf(m);
const parts=[];
if(m.credit_multiplier>0) parts.push('x'+trimN(m.credit_multiplier));
else if(!m.multiplier_known) parts.push('≈x1');
if(pr.i||pr.o) parts.push(fmtR(pr.i)+'/'+fmtR(pr.o));
if(!parts.length) return '<span class="muted">—</span>';
return '<span class="price">'+parts.join(' ')+'</span>';
}
// repPrice 用于最终价格排序的代表值：优先 Output 高价
function repPrice(m){
const pr=m._pr||prOf(m);
if(pr.o) return pr.o.hi;
if(pr.i) return pr.i.hi;
if(m.credit_multiplier>0) return Number(m.credit_multiplier);
return (m.cost_tier==='free'&&(!m.credit_multiplier||m.credit_multiplier===0))?0:null;
}

function multDisplay(m){
if(m.cost_tier==='free' && (!m.credit_multiplier || m.credit_multiplier===0)){
return '<span class="badge badge-free">0 (FREE)</span>';
}
if(!m.multiplier_known || m.credit_multiplier===0){
return '<span class="muted" title="上游未单独下发倍率，通常按基准 1.0">— / ≈1.0</span>';
}
const n=Number(m.credit_multiplier);
return 'x'+(n%1?n.toFixed(1):String(n));
}

function money(v){
if(v==null||v===undefined||v==='') return '<span class="muted">—</span>';
const n=Number(v);
if(Number.isNaN(n)) return '<span class="muted">—</span>';
return '<span class="price">$'+n.toFixed(n>=10?1:n>=1?2:3)+'</span>';
}

// promoInfo 生成优惠文案与截止倒计时
function promoInfo(m){
if(!(m.promo&&m.promo.active)) return null;
let tail='',tip='';
if(m.promo.end_date){
const diff=new Date(m.promo.end_date).getTime()-Date.now();
tip='截止 '+new Date(m.promo.end_date).toLocaleString();
if(diff>0){
const d=Math.floor(diff/864e5),h=Math.floor(diff%864e5/36e5),mi=Math.floor(diff%36e5/6e4);
if(d>0){tail='·剩'+d+'d'}else if(h>0){tail='·剩'+h+'h'}else{tail='·剩'+mi+'m'}
}else{tail='·已结束'}
}
return {tail:tail,tip:tip};
}

// discountRate 从促销文案解析折扣率："83% off"→0.83，"7折"→0.3；解析不出返回 null
function discountRate(label){
const off=/(\d+(?:\.\d+)?)\s*%\s*off/i.exec(label||'');
if(off) return Number(off[1])/100;
const zhe=/(\d+(?:\.\d+)?)\s*折/.exec(label||'');
if(zhe) return 1-Number(zhe[1])/10;
return null;
}
// origPrice 由折扣率反推原价：现价 = 原价 × (1-折扣率)
function origPrice(m){
const disc=discountRate(m.promo&&m.promo.label);
if(!disc||disc>=1) return null;
const scale=1/(1-disc);
const pr=m._pr||prOf(m);
const parts=[];
if(m.credit_multiplier>0) parts.push('x'+trimN(m.credit_multiplier*scale));
if(pr.i||pr.o){
const ri=pr.i?('$'+fmtP(pr.i.lo*scale)+(pr.i.lo===pr.i.hi?'':'~'+fmtP(pr.i.hi*scale))):'—';
const ro=pr.o?('$'+fmtP(pr.o.lo*scale)+(pr.o.lo===pr.o.hi?'':'~'+fmtP(pr.o.hi*scale))):'—';
parts.push(ri+'/'+ro);
}
return parts.length?parts.join(' '):null;
}
const GIFT_SVG='<svg viewBox="0 0 24 24" fill="none" stroke="#c084fc" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="8" width="18" height="4" rx="1"/><path d="M12 8v13"/><path d="M19 12v7a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2v-7"/><path d="M7.5 8a2.5 2.5 0 0 1 0-5C11 3 12 8 12 8s1-5 4.5-5a2.5 2.5 0 0 1 0 5"/></svg>';
// giftIcon 在模型名旁渲染礼物图标，悬浮显示促销详情（起止/剩余/原价划线/现价）
function giftIcon(m){
if(!(m.promo&&m.promo.active)) return '';
const end=m.promo.end_date?new Date(m.promo.end_date):null;
const pi=promoInfo(m);
let pop='<div class="pop">';
pop+='<div class="pt">'+(m.promo.label?esc(m.promo.label):'限时优惠')+'</div>';
pop+='<div><span class="pk">开始</span>—</div>';
pop+='<div><span class="pk">到期</span>'+(end?end.toLocaleString():'—')+'</div>';
pop+='<div><span class="pk">剩余</span>'+(pi&&pi.tail?esc(pi.tail.slice(1)):'—')+'</div>';
const orig=origPrice(m);
if(orig) pop+='<div><span class="pk">原价</span><span class="strike">'+orig+'</span></div>';
const pr=m._pr||prOf(m),cur=[];
if(m.credit_multiplier>0) cur.push('x'+trimN(m.credit_multiplier));
if(pr.i||pr.o) cur.push(fmtR(pr.i)+'/'+fmtR(pr.o));
if(cur.length) pop+='<div><span class="pk">现价</span>'+cur.join(' ')+'</div>';
pop+='</div>';
return '<span class="gift">'+GIFT_SVG+pop+'</span>';
}

function badges(m){
let b='';
if(m.cost_tier==='free') b+='<span class="badge badge-free">FREE</span>';
else if(m.cost_tier==='low') b+='<span class="badge badge-low">LOW</span>';
else if(m.cost_tier==='medium') b+='<span class="badge badge-medium">MEDIUM</span>';
else if(m.cost_tier==='high') b+='<span class="badge badge-high">HIGH</span>';
if(m.is_beta) b+='<span class="badge badge-beta">BETA</span>';
if(m.is_new) b+='<span class="badge badge-new">NEW</span>';
if(m.fast&&m.fast.active) b+='<span class="badge badge-fast" title="'+esc(m.fast.tooltip||'')+'">FAST</span>';
if(m.supports_images) b+='<span class="badge badge-img">img</span>';
if(m.is_premium) b+='<span class="badge badge-premium">Premium</span>';
if(m.is_recommended) b+='<span class="badge badge-rec">推荐</span>';
if(m.is_capacity_limited) b+='<span class="badge badge-cap">限容</span>';
if(m.is_command) b+='<span class="badge badge-cmd" title="命令目录模型（commit 信息等），不在 chat picker 但可调用">命令</span>';
if(m.disabled) b+='<span class="badge badge-off">禁用</span>';
return b;
}

// catChips 生成分类小标签：上游分组（≤3）+ 系列（去重）
function catChips(m){
const g=m._groups||[];
const fam=m.family&&g.indexOf(m.family)<0?m.family:null;
if(!g.length&&!fam) return '<span class="muted">—</span>';
let h='';
g.slice(0,3).forEach(x=>{h+='<span class="cat">'+esc(x)+'</span>'});
if(g.length>3) h+='<span class="cat" title="'+esc(g.join(' / '))+'">+'+(g.length-3)+'</span>';
if(fam) h+='<span class="cat cat-fam">'+esc(fam)+'</span>';
return h;
}

function matchTags(m){
for(const t of activeTags){
if(t==='free' && !(m.cost_tier==='free'&&(!m.credit_multiplier||m.credit_multiplier===0))) return false;
if(t==='promo' && !(m.promo&&m.promo.active)) return false;
if(t==='img' && !m.supports_images) return false;
if(t==='beta' && !m.is_beta) return false;
if(t==='new' && !m.is_new) return false;
if(t==='fast' && !(m.fast&&m.fast.active)) return false;
if(t==='premium' && !m.is_premium) return false;
if(t==='rec' && !m.is_recommended) return false;
if(t==='empty_mult'){
const empty=!m.multiplier_known || m.credit_multiplier===0;
if(!empty) return false;
}
if(t==='disabled' && !m.disabled) return false;
}
return true;
}

function multOf(m){
if(m.credit_multiplier>0) return Number(m.credit_multiplier);
if(m.cost_tier==='free') return 0;
return 1;
}

function applyFilters(){
const q=document.getElementById('search').value.trim().toLowerCase();
const group=document.getElementById('fGroup').value;
const provider=document.getElementById('fProvider').value;
const api=document.getElementById('fApi').value;
const tier=document.getElementById('fTier').value;
const pricing=document.getElementById('fPricing').value;
const sort=document.getElementById('fSort').value;
const merge=document.getElementById('fMerge').checked;

let list=allModels.filter(m=>{
if(group && (m._groups||[]).indexOf(group)<0) return false;
if(provider && m.provider!==provider) return false;
if(api && m.api_provider!==api) return false;
if(tier && m.cost_tier!==tier) return false;
if(pricing && m.pricing_type!==pricing) return false;
if(!matchTags(m)) return false;
if(q){
const hay=[m.uid,m.label,m.description,m.family,m.provider,m.api_provider].concat(m._groups||[]).join(' ').toLowerCase();
if(!hay.includes(q)) return false;
}
return true;
});

list=list.slice().sort((a,b)=>{
switch(sort){
case 'mult_asc': return multOf(a)-multOf(b);
case 'mult_desc': return multOf(b)-multOf(a);
case 'in_asc': return (a.price_input??1e9)-(b.price_input??1e9);
case 'in_desc': return (b.price_input??-1)-(a.price_input??-1);
case 'out_asc': return (a.price_output??1e9)-(b.price_output??1e9);
case 'out_desc': return (b.price_output??-1)-(a.price_output??-1);
case 'final_asc': return (repPrice(a)??1e9)-(repPrice(b)??1e9);
case 'final_desc': return (repPrice(b)??-1)-(repPrice(a)??-1);
case 'name': return String(a.label||a.uid).localeCompare(String(b.label||b.uid));
default: return 0;
}
});
renderModels(list,merge);
}

// renderModels 渲染模型表；merge 时同系列折叠为聚合行
function renderModels(models,merge){
const tbody=document.querySelector('#modelTable tbody');
const promoCount=allModels.filter(m=>m.promo&&m.promo.active).length;
document.getElementById('stats').innerHTML='显示 <b>'+models.length+'</b> / 共 '+allModels.length+' 个模型'+(promoCount?(' · 促销中 <b>'+promoCount+'</b>'):'');
if(!models.length){
tbody.innerHTML='<tr><td colspan="12" class="loading">无匹配模型</td></tr>';
return;
}
if(merge){
renderFamilies(models,tbody);
return;
}
let html='';
models.forEach((m,i)=>{
const dimTip=(m.dimensions||[]).map(d=>d.label+': '+d.value+(d.min||d.max?(' (min '+d.min+' ~ max '+d.max+')'):'')+' / '+(d.denominator||'')).join(' | ');
const title=[m.description,m.family?('系列: '+m.family):'',m.max_tokens?('上下文上限: '+m.max_tokens+' tokens'):'',dimTip,m.beta_warning||''].filter(Boolean).join(' | ');
html+='<tr title="'+esc(title)+'">';
html+='<td class="muted">'+(i+1)+'</td>';
html+='<td><div><strong>'+esc(m.label||'-')+'</strong>'+giftIcon(m)+'</div><div class="mono muted">'+esc(m.uid)+'</div></td>';
html+='<td>'+catChips(m)+'</td>';
html+='<td>'+esc(m.provider||'-');
if(m.api_provider && m.api_provider!==m.provider){
html+='<div class="muted mono">'+esc(m.api_provider)+'</div>';
}
html+='</td>';
html+='<td>'+esc(m.cost_tier||'-')+'</td>';
html+='<td>'+multDisplay(m)+'</td>';
html+='<td>'+money(m.price_input)+'</td>';
html+='<td>'+money(m.price_cached)+'</td>';
html+='<td>'+money(m.price_output)+'</td>';
html+='<td>'+finalPrice(m)+'</td>';
html+='<td class="mono">'+esc(m.pricing_type||'-')+'</td>';
html+='<td>'+badges(m)+'</td>';
html+='</tr>';
});
tbody.innerHTML=html;
}

// renderFamilies 把同系列模型折叠为一行：价格取成员区间并集
function renderFamilies(models,tbody){
const fams=[],seen={};
models.forEach(m=>{
const k=m.family||m.uid;
if(!seen[k]){seen[k]={key:k,mem:[]};fams.push(seen[k])}
seen[k].mem.push(m);
});
let html='';
fams.forEach((f,i)=>{
const mem=f.mem,one=mem.length===1,m=mem[0];
// 聚合维度
const providers=[...new Set(mem.map(x=>x.provider).filter(Boolean))];
const apis=[...new Set(mem.map(x=>x.api_provider).filter(Boolean))];
const tiers=[...new Set(mem.map(x=>x.cost_tier).filter(Boolean))];
const pricings=[...new Set(mem.map(x=>x.pricing_type).filter(Boolean))];
const groups=[...new Set(mem.flatMap(x=>x._groups||[]))];
const fam={_groups:groups,family:m.family};
const priceAgg={i:null,c:null,o:null};
['i','c','o'].forEach(k=>{
const rs=mem.map(x=>(x._pr||prOf(x))[k]).filter(Boolean);
if(rs.length) priceAgg[k]={lo:Math.min(...rs.map(r=>r.lo)),hi:Math.max(...rs.map(r=>r.hi))};
});
const mults=mem.map(x=>Number(x.credit_multiplier)||0);
const multLo=Math.min(...mults),multHi=Math.max(...mults);
const dimTip=mem.map(x=>x.label+': '+(x.uid||'')).join(' | ');
const def=mem.find(x=>x.is_default_in_family);
html+='<tr'+(one?'':' class="fam"')+' title="'+esc(dimTip)+'">';
html+='<td class="muted">'+(i+1)+'</td>';
const promoMem=mem.find(x=>x.promo&&x.promo.active);
html+='<td><div><strong>'+esc(m.family||m.label||'-')+'</strong>'+(promoMem?giftIcon(promoMem):'')+(one?'':' <span class="badge badge-rec">'+mem.length+' 个模型</span>')+'</div><div class="mono muted">'+esc(one?m.uid:(def?def.uid+' (默认)':''))+'</div></td>';
html+='<td>'+catChips(fam)+'</td>';
html+='<td>'+esc(providers.join(' / ')||'-');
if(apis.length&&(apis.join('/')!==providers.join('/'))){
html+='<div class="muted mono">'+esc(apis.join(' / '))+'</div>';
}
html+='</td>';
html+='<td>'+esc(tiers.join(' / ')||'-')+'</td>';
html+='<td>'+(multLo===multHi?('x'+trimN(multLo)):('x'+trimN(multLo)+'~'+trimN(multHi)))+'</td>';
html+='<td><span class="price">'+(priceAgg.i?fmtR(priceAgg.i):'—')+'</span></td>';
html+='<td><span class="price">'+(priceAgg.c?fmtR(priceAgg.c):'—')+'</span></td>';
html+='<td><span class="price">'+(priceAgg.o?fmtR(priceAgg.o):'—')+'</span></td>';
// 家族最终价格：倍率区间 + 价格区间
html+='<td>'+(one?finalPrice(m):('<span class="price">'+(multLo===multHi?('x'+trimN(multLo)):('x'+trimN(multLo)+'~'+trimN(multHi)))+' '+fmtR(priceAgg.i)+'/'+fmtR(priceAgg.o)+'</span>'))+'</td>';
html+='<td class="mono">'+esc(pricings.join(' / ')||'-')+'</td>';
html+='<td>'+badges(m)+(one?'':'<div class="muted" style="font-size:10px;margin-top:2px">'+mem.slice(1).map(x=>esc(x.label||x.uid)).join(' · ')+'</div>')+'</td>';
html+='</tr>';
});
tbody.innerHTML=html;
}

async function loadModels(){
const res=await fetch('/panel/api/models');
const data=await res.json();
allModels=data.models||[];
// 分类映射：分组 model_labels → 组名；同时按 uid 兜底
const label2groups={};
(data.model_sorts||[]).forEach(s=>{
if(s.display_metric) return; // 度量排序（如按输入价排序）不是分类
(s.groups||[]).forEach(g=>{
const cat=g.name||s.name;
if(!cat) return;
(g.model_labels||[]).forEach(l=>{
const key=String(l);
(label2groups[key]=label2groups[key]||[]).push(cat);
});
});
});
const groupNames=new Set(), providers=new Set(), apis=new Set(), pricings=new Set();
allModels.forEach(m=>{
m._groups=[...new Set(label2groups[m.label]||label2groups[m.uid]||[])];
m._pr=prOf(m);
(m._groups||[]).forEach(g=>groupNames.add(g));
if(m.provider) providers.add(m.provider);
if(m.api_provider) apis.add(m.api_provider);
if(m.pricing_type) pricings.add(m.pricing_type);
});
fillSelect('fGroup',groupNames);
fillSelect('fProvider',providers);
fillSelect('fApi',apis);
fillSelect('fPricing',pricings);
applyFilters();
}

// ---- 统计页 ----
function showPage(name){
document.getElementById('pageStats').style.display=name==='stats'?'':'none';
document.getElementById('pageModels').style.display=name==='models'?'':'none';
document.getElementById('tabStats').classList.toggle('on',name==='stats');
document.getElementById('tabModels').classList.toggle('on',name==='models');
}
// fmtTok 大数字缩写：1234567 → 1.23M，12.3e9 → 12.3B
function fmtTok(v){
const n=Number(v)||0;
if(n>=1e9) return (n/1e9).toFixed(2)+'B';
if(n>=1e6) return (n/1e6).toFixed(2)+'M';
if(n>=1e3) return (n/1e3).toFixed(1)+'K';
return String(n);
}
// fmtDur 秒数 → Xd Xh Xm
function fmtDur(sec){
sec=Number(sec)||0;
const d=Math.floor(sec/86400),h=Math.floor(sec%86400/3600),m=Math.floor(sec%3600/60),s=Math.floor(sec%60);
if(d>0) return d+'d '+h+'h';
if(h>0) return h+'h '+m+'m';
if(m>0) return m+'m '+s+'s';
return s+'s';
}
function fmtMS(ms){
ms=Number(ms)||0;
return ms>=1000?((ms/1000).toFixed(1)+'s'):(ms+'ms');
}

async function loadStats(){
let data;
try{data=await(await fetch('/panel/api/stats')).json()}catch(e){return}
const t=data.totals||{};
const cards=document.getElementById('statCards');
let html='';
html+=card('总请求',fmtTok(t.requests||0));
html+=card('成功率',(data.success_rate!=null?(data.success_rate*100).toFixed(1):'0')+'%');
html+=card('总 Token',fmtTok(t.total_tokens||0));
html+=card('输入',fmtTok(t.input||0));
html+=card('输出',fmtTok(t.output||0));
html+=card('缓存 读/写',fmtTok(t.cache_read||0)+' / '+fmtTok(t.cache_write||0));
html+=card('平均耗时',fmtMS(data.avg_ms||0));
html+=card('在线时长',fmtDur(data.uptime_sec||0));
// 模型请求 TOP
const models=data.models||{};
const top=Object.entries(models).sort((a,b)=>b[1].requests-a[1].requests).slice(0,6);
if(top.length){
html+=top.map(([m,s])=>'<div class="card"><div class="label" title="'+esc(m)+'">'+esc(m)+'</div><div class="value">'+s.requests+' 次</div><div class="sub">'+fmtTok(s.total)+' tok</div></div>').join('');
}
cards.innerHTML=html;
drawChart(data.buckets||[]);
// 最近 20 条
const tbody=document.querySelector('#recentTable tbody');
const rec=data.recent||[];
if(!rec.length){tbody.innerHTML='<tr><td colspan="10" class="loading">暂无数据</td></tr>';return}
tbody.innerHTML=rec.map((e,i)=>{
const cls=e.result==='completed'?'result-ok':e.result==='disconnected'?'result-warn':'result-bad';
const row='<tr class="rec-row" onclick="toggleDetail('+i+')"><td class="mono muted">'+new Date(e.time).toLocaleTimeString()+'</td><td class="mono muted">'+esc(e.ip||'-')+'</td><td class="mono muted">'+esc(e.path||'-')+'</td><td class="mono">'+esc(e.model||'-')+'</td><td class="'+cls+'">'+esc(e.result||'-')+(e.status?(' <span class="muted">'+e.status+'</span>'):'')+'</td><td>'+fmtMS(e.duration_ms)+'</td><td>'+fmtTok(e.input)+'</td><td>'+fmtTok(e.output)+'</td><td>'+fmtTok(e.cache_read)+'/'+fmtTok(e.cache_write)+'</td><td>'+fmtTok(e.total)+'</td></tr>';
return row+'<tr class="detail" id="recDetail'+i+'" style="display:none"><td colspan="10"><div class="detail-grid"><div><div class="detail-h">请求头</div><pre>'+esc(headerText(e.req_headers))+'</pre></div><div><div class="detail-h">请求体</div><pre>'+esc(pretty(e.req_body))+'</pre></div><div><div class="detail-h">响应</div><pre>'+esc(pretty(e.resp_body))+'</pre></div></div></td></tr>';
}).join('');
}

// headerText 把请求头 map 排成 k: v 多行文本
function headerText(h){
if(!h) return '-';
return Object.keys(h).sort().map(k=>k+': '+h[k]).join('\n')||'-';
}
// pretty 尝试 JSON 美化，失败返回原文
function pretty(s){
if(!s) return '-';
try{return JSON.stringify(JSON.parse(s),null,2)}catch(e){return s}
}
function toggleDetail(i){
const el=document.getElementById('recDetail'+i);
if(el) el.style.display=el.style.display==='none'?'':'none';
}

// 图表悬浮状态：数据与几何参数供 mousemove 重绘 tooltip
let chartHours=[],chartGeom=null,chartHover=-1,chartBucketSrc=[];

// drawChart 绘制最近 24 小时：堆叠柱(输入+输出 token) + 折线(请求数) + 悬浮 tooltip
function drawChart(buckets){
chartBucketSrc=buckets;
const cv=document.getElementById('statChart');
if(!cv) return;
const dpr=window.devicePixelRatio||1;
const w=cv.clientWidth,h=cv.clientHeight;
if(w===0) return;
cv.width=w*dpr;cv.height=h*dpr;
const ctx=cv.getContext('2d');
ctx.scale(dpr,dpr);
ctx.clearRect(0,0,w,h);
// 补全最近 24 小时的连续桶
const byHour={};
buckets.forEach(b=>{byHour[Math.floor(b.hour/3600)]=b});
const now=Math.floor(Date.now()/3600000);
const hours=[];
for(let i=23;i>=0;i--){
const k=now-i;
const b=byHour[k]||{requests:0,input:0,output:0,cached:0};
hours.push({k:k,r:b.requests||0,i:b.input||0,o:b.output||0,c:b.cached||0});
}
chartHours=hours;
const maxTok=Math.max(1,...hours.map(x=>x.i+x.o));
const maxReq=Math.max(1,...hours.map(x=>x.r));
const padL=44,padR=44,padT=10,padB=20;
const cw=(w-padL-padR)/24,bw=Math.min(cw*0.6,26);
const chartH=h-padT-padB;
chartGeom={padL:padL,padR:padR,padT:padT,padB:padB,cw:cw,chartH:chartH,w:w,h:h};
// 网格线
ctx.strokeStyle='#232a3a';ctx.lineWidth=1;
ctx.fillStyle='#59637a';ctx.font='9px sans-serif';ctx.textAlign='left';
for(let g=0;g<=4;g++){
const y=padT+chartH*(1-g/4);
ctx.beginPath();ctx.moveTo(padL,y);ctx.lineTo(w-padR,y);ctx.stroke();
ctx.fillText(fmtTok(Math.round(maxTok*g/4)),4,y+3);
ctx.fillStyle='#59637a';
const rv=Math.round(maxReq*g/4);
ctx.textAlign='right';ctx.fillText(String(rv),w-4,y+3);ctx.textAlign='left';
}
// 柱状
hours.forEach((b,idx)=>{
const x=padL+idx*cw+(cw-bw)/2;
const ih=chartH*(b.i/maxTok),oh=chartH*(b.o/maxTok);
ctx.fillStyle='#5b8cff';
ctx.fillRect(x,padT+chartH-ih,bw,ih);
ctx.fillStyle='#22d3ee';
ctx.fillRect(x,padT+chartH-ih-oh,bw,oh);
});
// 折线 请求数
ctx.strokeStyle='#fbbf24';ctx.lineWidth=1.6;ctx.beginPath();
hours.forEach((b,idx)=>{
const x=padL+idx*cw+cw/2,y=padT+chartH*(1-b.r/maxReq);
if(idx===0)ctx.moveTo(x,y);else ctx.lineTo(x,y);
});
ctx.stroke();
// X 轴标签（每 6 小时）
ctx.fillStyle='#59637a';ctx.textAlign='center';
hours.forEach((b,idx)=>{
if(idx%6!==0) return;
const d=new Date(b.k*3600000);
ctx.fillText(String(d.getHours()).padStart(2,'0')+':00',padL+idx*cw+cw/2,h-6);
});
// 悬浮 tooltip 覆盖层
drawChartHover(ctx);
// 悬浮监听只需挂一次
if(!cv._hoverBound){
cv._hoverBound=true;
cv.addEventListener('mousemove',e=>{
const g=chartGeom;if(!g)return;
const rect=cv.getBoundingClientRect();
const idx=Math.floor((e.clientX-rect.left-g.padL)/g.cw);
const next=(idx>=0&&idx<chartHours.length)?idx:-1;
if(next!==chartHover){chartHover=next;drawChart(chartBucketSrc);}
});
cv.addEventListener('mouseleave',()=>{chartHover=-1;drawChart(chartBucketSrc);});
}
}

// drawChartHover 在 chartHover 列上绘制高亮竖线与数据 tooltip
function drawChartHover(ctx){
const g=chartGeom;
if(!g||chartHover<0||chartHover>=chartHours.length)return;
const b=chartHours[chartHover];
const cx=g.padL+chartHover*g.cw+g.cw/2;
// 列高亮 + 竖线
ctx.fillStyle='rgba(124,138,255,0.07)';
ctx.fillRect(g.padL+chartHover*g.cw,g.padT,g.cw,g.chartH);
ctx.strokeStyle='#7c8aff';ctx.lineWidth=1;
ctx.beginPath();ctx.moveTo(cx,g.padT);ctx.lineTo(cx,g.padT+g.chartH);ctx.stroke();
// tooltip
const d=new Date(b.k*3600000);
const lines=[(d.getMonth()+1)+'/'+d.getDate()+' '+String(d.getHours()).padStart(2,'0')+':00',
'输入 '+fmtTok(b.i),'输出 '+fmtTok(b.o),'缓存 '+fmtTok(b.c),'请求 '+b.r];
ctx.font='10px sans-serif';
const tw=Math.max(...lines.map(l=>ctx.measureText(l).width))+18;
const th=lines.length*15+10;
let tx=cx+8;if(tx+tw>g.w-4)tx=cx-tw-8;
const ty=g.padT+4;
ctx.fillStyle='rgba(20,25,38,0.96)';
ctx.fillRect(tx,ty,tw,th);
ctx.strokeStyle='#3a4560';
ctx.strokeRect(tx,ty,tw,th);
ctx.fillStyle='#dde2ee';ctx.textAlign='left';
lines.forEach((l,i)=>{ctx.fillText(l,tx+9,ty+17+i*15)});
}

function refreshAll(){loadStatus();loadModels();loadStats()}

loadStatus();
loadModels();
loadStats();
setInterval(loadStats,15000);
</script>
</body>
</html>`
