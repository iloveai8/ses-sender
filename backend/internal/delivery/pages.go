package delivery

// 退订页 HTML 模板（由金标准反推的逐字节保真模板——脚本生成，勿手改；
// 渲染入口在 unsub.go；__XXX__ 占位标记，空值渲染空串）
const pageTemplateHTML = `<!DOCTYPE html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>__T__</title>
<style>
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;display:flex;justify-content:center;align-items:center;min-height:100vh;background:linear-gradient(135deg,__COLOR__ 0%,__COLOR__cc 100%);padding:20px}
.card{background:#fff;border-radius:20px;padding:40px;max-width:520px;width:100%;box-shadow:0 20px 60px rgba(0,0,0,.15)}
h1{font-size:22px;color:#1f2937;margin-bottom:8px}
.subtitle{color:#6b7280;font-size:14px;margin-bottom:24px;line-height:1.5}
.email-info{background:#f3f4f6;border-radius:10px;padding:14px 18px;margin-bottom:24px;font-size:13px;color:#4b5563}
.email-info strong{color:#111827}
h3{font-size:14px;color:#374151;margin-bottom:12px}
.reasons{display:flex;flex-direction:column;gap:8px;margin-bottom:20px}
.reason{display:flex;align-items:center;gap:10px;padding:12px 16px;border:2px solid #e5e7eb;border-radius:10px;cursor:pointer;transition:all .2s}
.reason:hover{border-color:__COLOR__88;background:__COLOR__08}
.reason input{accent-color:__COLOR__;width:16px;height:16px}
.reason label{font-size:14px;color:#374151;cursor:pointer;flex:1}
.reason.selected{border-color:__COLOR__;background:__COLOR__08}
.other-input{width:100%;border:2px solid #e5e7eb;border-radius:8px;padding:10px 14px;font-size:13px;margin-top:8px;display:none;outline:none;transition:border .2s}
.other-input:focus{border-color:__COLOR__}
.btn{width:100%;padding:14px;border:none;border-radius:10px;font-size:15px;font-weight:600;cursor:pointer;transition:all .2s}
.btn-primary{background:#ef4444;color:#fff}.btn-primary:hover{background:#dc2626}
.btn-primary:disabled{background:#d1d5db;cursor:not-allowed}
.btn-secondary{background:#f3f4f6;color:#6b7280;margin-top:10px}.btn-secondary:hover{background:#e5e7eb}
.success{display:none;text-align:center}
.success h2{color:#10b981;font-size:24px;margin:16px 0 8px}
.success p{color:#6b7280;font-size:14px;line-height:1.6}
</style>
</head><body>
<div class="card">
  <div id="form-view">
    __LOGO1__
    <h1>__T__</h1>
    <p class="subtitle">__SUB__</p>
    <div class="email-info">
      Email Address : <strong>__EMAIL__</strong><br>
      Sender : <strong>__SRC__</strong>
    </div>
    <h3>Unsubscribe Reason :</h3>
    <div class="reasons">
      __REASONS__
    </div>
    <input id="other-text" class="other-input" placeholder="Please enter another reason..." maxlength="200">
    <button class="btn btn-primary" id="confirm-btn" onclick="doUnsubscribe()">__BTN__</button>
    <button class="btn btn-secondary" onclick="window.close()">Cancel</button>
  </div>
  <div class="success" id="success-view">
    __LOGO2__
    <p style="font-size:48px">✅</p>
    <h2>__SUCCESS__</h2>
    <p><strong>__EMAIL__</strong> will no longer receive emails from <strong>__SRC__</strong>.</p>
    <p style="margin-top:16px;color:#9ca3af;font-size:13px">Thank you for your feedback.</p>
  </div>
</div>
<script>
let selectedReason='';
function selectReason(el,val){
  document.querySelectorAll('.reason').forEach(r=>r.classList.remove('selected'));
  el.classList.add('selected');
  el.querySelector('input').checked=true;
  selectedReason=val;
  document.getElementById('other-text').style.display=val==='other'?'block':'none';
}
async function doUnsubscribe(){
  const btn=document.getElementById('confirm-btn');
  btn.disabled=true;btn.textContent='Processing...';
  const reason=selectedReason==='other'?('other:'+document.getElementById('other-text').value):selectedReason;
  try{
    const fd=new FormData();
    fd.append('token','__TOKEN__');
    fd.append('reason',reason||'web-unsubscribe');
    await fetch(window.location.pathname,{method:'POST',body:fd});
    document.getElementById('form-view').style.display='none';
    document.getElementById('success-view').style.display='block';
  }catch{btn.disabled=false;btn.textContent='__BTN__';alert('Something went wrong, please try again');}
}
</script>
</div></body></html>`

const alreadyPageHTML = `<!DOCTYPE html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>__T__</title>
<style>
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;display:flex;justify-content:center;align-items:center;min-height:100vh;background:linear-gradient(135deg,#667eea 0%,#667eeacc 100%);padding:20px}
.card{background:#fff;border-radius:20px;padding:40px;max-width:520px;width:100%;box-shadow:0 20px 60px rgba(0,0,0,.15)}
h1{font-size:22px;color:#1f2937;margin-bottom:8px}
.subtitle{color:#6b7280;font-size:14px;margin-bottom:24px;line-height:1.5}
.email-info{background:#f3f4f6;border-radius:10px;padding:14px 18px;margin-bottom:24px;font-size:13px;color:#4b5563}
.email-info strong{color:#111827}
h3{font-size:14px;color:#374151;margin-bottom:12px}
.reasons{display:flex;flex-direction:column;gap:8px;margin-bottom:20px}
.reason{display:flex;align-items:center;gap:10px;padding:12px 16px;border:2px solid #e5e7eb;border-radius:10px;cursor:pointer;transition:all .2s}
.reason:hover{border-color:#667eea88;background:#667eea08}
.reason input{accent-color:#667eea;width:16px;height:16px}
.reason label{font-size:14px;color:#374151;cursor:pointer;flex:1}
.reason.selected{border-color:#667eea;background:#667eea08}
.other-input{width:100%;border:2px solid #e5e7eb;border-radius:8px;padding:10px 14px;font-size:13px;margin-top:8px;display:none;outline:none;transition:border .2s}
.other-input:focus{border-color:#667eea}
.btn{width:100%;padding:14px;border:none;border-radius:10px;font-size:15px;font-weight:600;cursor:pointer;transition:all .2s}
.btn-primary{background:#ef4444;color:#fff}.btn-primary:hover{background:#dc2626}
.btn-primary:disabled{background:#d1d5db;cursor:not-allowed}
.btn-secondary{background:#f3f4f6;color:#6b7280;margin-top:10px}.btn-secondary:hover{background:#e5e7eb}
.success{display:none;text-align:center}
.success h2{color:#10b981;font-size:24px;margin:16px 0 8px}
.success p{color:#6b7280;font-size:14px;line-height:1.6}
</style>
</head><body>
<div class="card">
  <div id="form-view">
    
    <h1>__T__</h1>
    <p class="subtitle">我们很遗憾看到您离开。请告诉我们退订原因，帮助我们改进服务。</p>
    <div class="email-info">
      Email Address : <strong>__EMAIL__</strong><br>
      Sender : <strong>__SRC__</strong>
    </div>
    <h3>Unsubscribe Reason :</h3>
    <div class="reasons">
      <div class="reason" onclick="selectReason(this,'too_frequent')"><input type="radio" name="reason" value="too_frequent"><label>收到邮件太频繁</label></div>
<div class="reason" onclick="selectReason(this,'not_relevant')"><input type="radio" name="reason" value="not_relevant"><label>内容与我无关</label></div>
<div class="reason" onclick="selectReason(this,'never_subscribed')"><input type="radio" name="reason" value="never_subscribed"><label>我从未订阅过</label></div>
<div class="reason" onclick="selectReason(this,'prefer_other')"><input type="radio" name="reason" value="prefer_other"><label>我更喜欢其他渠道获取信息</label></div>
<div class="reason" onclick="selectReason(this,'other')"><input type="radio" name="reason" value="other"><label>其他原因</label></div>

    </div>
    <input id="other-text" class="other-input" placeholder="Please enter another reason..." maxlength="200">
    <button class="btn btn-primary" id="confirm-btn" onclick="doUnsubscribe()">确认退订</button>
    <button class="btn btn-secondary" onclick="window.close()">Cancel</button>
  </div>
  <div class="success" id="success-view">
    __LOGO__
    <p style="font-size:48px">✅</p>
    <h2>退订成功</h2>
    <p><strong>__EMAIL__</strong> will no longer receive emails from <strong>__SRC__</strong>.</p>
    <p style="margin-top:16px;color:#9ca3af;font-size:13px">Thank you for your feedback.</p>
  </div>
</div>
<script>
let selectedReason='';
function selectReason(el,val){
  document.querySelectorAll('.reason').forEach(r=>r.classList.remove('selected'));
  el.classList.add('selected');
  el.querySelector('input').checked=true;
  selectedReason=val;
  document.getElementById('other-text').style.display=val==='other'?'block':'none';
}
async function doUnsubscribe(){
  const btn=document.getElementById('confirm-btn');
  btn.disabled=true;btn.textContent='Processing...';
  const reason=selectedReason==='other'?('other:'+document.getElementById('other-text').value):selectedReason;
  try{
    const fd=new FormData();
    fd.append('token','dW5zdWItc2VlZEBoYXJuZXNzLmxvY2FsfGFkbWluQHNlZWQubG9jYWx8NDk4MTA5ZWE4MzVmYWY4MA==');
    fd.append('reason',reason||'web-unsubscribe');
    await fetch(window.location.pathname,{method:'POST',body:fd});
    document.getElementById('form-view').style.display='none';
    document.getElementById('success-view').style.display='block';
  }catch{btn.disabled=false;btn.textContent='确认退订';alert('Something went wrong, please try again');}
}
</script>
</div></body></html>`
