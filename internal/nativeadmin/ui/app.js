let csrf = '';
let timer;
let qrRun = 0;
let currentVerifyKey = '';
const $ = id => document.getElementById(id);
const notice = text => { $('notice').textContent = text; };

async function api(path, body, method = "POST") {
  const options = { credentials: 'same-origin', headers: {} };
  if (body !== undefined) {
    options.method = method;
    options.headers['Content-Type'] = 'application/json';
    if (csrf) options.headers['X-CSRF-Token'] = csrf;
    options.body = JSON.stringify(body);
  }
  const response = await fetch(path.startsWith('/') ? path : '/admin/api/' + path, options);
  const result = await response.json();
  if (!response.ok) throw new Error(result.error || '请求失败');
  return result;
}

function closeQRVerification() {
  currentVerifyKey = '';
  $('qr-verify').hidden = true;
  $('qr-verify').removeAttribute('src');
}

window.addEventListener('message', event => {
  if (event.origin !== 'null' || event.source !== $('qr-verify').contentWindow ||
      event.data?.type !== 'qishui-second-verify-complete' || event.data?.key !== currentVerifyKey) return;
  closeQRVerification();
  poll(qrRun);
});

function showLogin() {
  closeQRVerification();
  resetQQWindow();
  clearTimeout(timer);
  qrRun++;
  csrf = '';
  $('new-token').textContent = '';
  $('new-token').hidden = true;
  $('webhook-form').reset();
  $('dashboard').hidden = true;
  $('login').hidden = false;
  $('qr').hidden = true;
}

async function loadAccounts() {
  const [result, pending] = await Promise.all([api('accounts'), api('/admin/cookies/tencent-verifications')]);
  const challenges = new Set(pending.data.map(account => account.id));
  const list = $('accounts');
  list.replaceChildren();
  for (const account of result.accounts) {
    const item = document.createElement('li');
    const description = document.createElement('span');
    description.textContent = [account.platform, account.nickname || '未命名账号',
      account.isValid ? '有效' : '需重新登录', account.isVip ? '会员' : '普通',
      account.isActive ? '已启用' : '已停用'].join(' · ');
    const button = document.createElement('button');
    button.type = 'button';
    button.textContent = account.isActive ? '停用' : '启用';
    button.addEventListener('click', async () => {
      button.disabled = true;
      try { await api('accounts/active', { id: account.id, active: !account.isActive }); await loadAccounts(); }
      catch (error) { notice(error.message); button.disabled = false; }
    });
    const verify = document.createElement('button');
    verify.type = 'button';
    verify.textContent = '验证';
    verify.addEventListener('click', async () => {
      try { await api('/admin/cookies/' + encodeURIComponent(account.id) + '/verify', {}); await loadAccounts(); }
      catch (error) { notice(error.message); }
    });
    const remove = document.createElement('button');
    remove.type = 'button';
    remove.textContent = '删除';
    remove.addEventListener('click', async () => {
      if (!confirm('删除此音源账号？')) return;
      try { await api('/admin/cookies/' + encodeURIComponent(account.id), {}, 'DELETE'); await loadAccounts(); }
      catch (error) { notice(error.message); }
    });
    item.append(description, button, verify);
    if (account.platform === 'tencent') {
      const renew = document.createElement('button');
      renew.type = 'button';
      renew.textContent = '续期';
      renew.addEventListener('click', async () => {
        try {
          const result = await api('/admin/cookies/' + encodeURIComponent(account.id) + '/refresh', {});
          await loadAccounts();
          notice(result.data.validated ? 'QQ 音乐账号已续期' : '新凭据已保存，请重新验证账号');
        } catch (error) { notice(error.message); }
      });
      item.append(renew);
    }
    if (challenges.has(account.id)) {
      const challenge = document.createElement('button');
      challenge.type = 'button';
      challenge.textContent = '打开 QQ 验证';
      challenge.addEventListener('click', () => openQQWindow(account.id).catch(error => notice(error.message)));
      item.append(challenge);
    }
    item.append(remove);
    list.append(item);
  }
  if (!result.accounts.length) list.textContent = '暂无账号';
}

$('account-form').addEventListener('submit', async event => {
  event.preventDefault();
  const form = new FormData(event.currentTarget);
  try {
    await api('/admin/cookies', { platform: form.get('platform'), cookie: form.get('cookie'), note: form.get('note') });
    event.currentTarget.reset();
    notice('音源账号已保存');
    await loadAccounts();
  } catch (error) { notice(error.message); }
});

async function showDashboard(role) {
  $('login').hidden = true;
  $('dashboard').hidden = false;
  notice('');
  $('admin-sources').hidden = role !== 'admin';
  await Promise.all(role === 'admin' ? [loadAccounts(), loadOperations(), loadTwoFactor()] : [loadTwoFactor()]);
}

$('login-form').addEventListener('submit', async event => {
  event.preventDefault();
  const form = new FormData(event.currentTarget);
  try {
    const result = await api('login', { username: form.get('username'), password: form.get('password'), code: form.get('code') || '' });
    if (result.require2FA) { $('code-label').hidden = false; notice('请输入双重验证码'); return; }
    csrf = result.csrf;
    event.currentTarget.reset();
    await showDashboard(result.role);
  } catch (error) { notice(error.message); }
});

$('logout').addEventListener('click', async () => {
  try { await api('logout', {}); } catch (_) {}
  showLogin();
  notice('已退出登录');
});

async function loadTwoFactor() {
  const result = await api('2fa');
  $('two-factor-status').textContent = result.enabled ? '已启用' : '未启用';
  $('two-factor-setup').hidden = result.enabled;
  $('two-factor-disable').hidden = !result.enabled;
  $('password-code-label').hidden = !result.enabled;
  $('two-factor-enable').hidden = true;
  $('two-factor-secret').textContent = '';
  $('two-factor-image').removeAttribute('src');
}

$('password-form').addEventListener('submit', async event => {
  event.preventDefault();
  const form = new FormData(event.currentTarget);
  try {
    await api('password', { oldPassword: form.get('oldPassword'), newPassword: form.get('newPassword'), code: form.get('code') || '' });
    event.currentTarget.reset();
    notice('管理密码已更新');
  } catch (error) { notice(error.message); }
});

$('two-factor-setup').addEventListener('click', async () => {
  try {
    const result = await api('2fa/setup', {});
    $('two-factor-secret').textContent = result.secret;
    $('two-factor-image').src = '/admin/api/2fa/image?t=' + Date.now();
    $('two-factor-enable').hidden = false;
  } catch (error) { notice(error.message); }
});

$('two-factor-enable').addEventListener('submit', async event => {
  event.preventDefault();
  const form = new FormData(event.currentTarget);
  try { await api('2fa/enable', { code: form.get('code') }); event.currentTarget.reset(); await loadTwoFactor(); notice('双重验证已启用'); }
  catch (error) { notice(error.message); }
});

$('two-factor-disable').addEventListener('submit', async event => {
  event.preventDefault();
  const form = new FormData(event.currentTarget);
  try { await api('2fa/disable', { password: form.get('password'), code: form.get('code') }); event.currentTarget.reset(); await loadTwoFactor(); notice('双重验证已关闭'); }
  catch (error) { notice(error.message); }
});

async function poll(run) {
  try {
    const result = await api('qr/poll', {});
    if (run !== qrRun) return;
    if (result.status === 'confirmed') {
      $('qr-status').textContent = '登录成功，账号已保存。';
      await loadAccounts();
    } else if (result.status === 'second_verify') {
      $('qr-status').textContent = '请完成汽水音乐二次验证。';
      if (result.verifyKey && result.verifyKey !== currentVerifyKey) {
        currentVerifyKey = result.verifyKey;
        $('qr-verify').src = '/admin/qr/qishui/security/security_host.html?key=' + encodeURIComponent(currentVerifyKey);
        $('qr-verify').hidden = false;
      }
    } else if (result.status === 'error') {
      $('qr-status').textContent = '扫码连接或登录失败，请重试。';
    } else if (result.status === 'expired') {
      $('qr-status').textContent = '二维码已过期，请重新生成。';
    } else {
      $('qr-status').textContent = result.status === 'scanned' ? '已扫码，请在手机上确认。' : '请使用所选音乐 App 扫码。';
      timer = setTimeout(() => poll(run), 2500);
    }
  } catch (error) { if (run === qrRun) $('qr-status').textContent = error.message; }
}

$('create-qr').addEventListener('click', async () => {
  clearTimeout(timer);
  const run = ++qrRun;
  closeQRVerification();
  $('create-qr').disabled = true;
  try {
    await api('qr/create', { platform: $('qr-platform').value });
    $('qr').hidden = false;
    $('qr-status').textContent = '请使用所选音乐 App 扫码。';
    $('qr-image').src = '/admin/api/qr/image?t=' + Date.now();
    timer = setTimeout(() => poll(run), 2500);
  } catch (error) { notice(error.message); }
  finally { $('create-qr').disabled = false; }
});

api('session').then(result => { csrf = result.csrf; return showDashboard(result.role); }).catch(showLogin);

let qqAccount = '';
let qqFrameTimer;
let qqDrag = false;
let qqPointerChain = Promise.resolve();
let qqLastMove = 0;
const frame = $('qq-frame');
const qqPath = action => '/admin/cookies/' + encodeURIComponent(qqAccount) + '/' + action;

function pollQQFrame() {
  if (!qqAccount) return;
  frame.src = qqPath('verification/frame') + '?t=' + Date.now();
  clearTimeout(qqFrameTimer);
  qqFrameTimer = setTimeout(pollQQFrame, 400);
}

function resetQQWindow() {
  clearTimeout(qqFrameTimer);
  qqAccount = '';
  qqDrag = false;
  frame.removeAttribute('src');
  $('qq-verify').hidden = true;
}

async function openQQWindow(id) {
  qqAccount = id;
  try {
    await api(qqPath('verification/start'), {});
    $('qq-verify').hidden = false;
    pollQQFrame();
  } catch (error) { resetQQWindow(); throw error; }
}

$('qq-close').addEventListener('click', async () => {
  try { await api(qqPath('verification/close'), {}); } catch (_) {}
  resetQQWindow();
});

$('qq-retry').addEventListener('click', async () => {
  try {
    const result = await api(qqPath('retry-play'), {});
    if (!result.success) { notice(result.error); return; }
    resetQQWindow();
    await loadAccounts();
    notice('QQ 音乐播放链接已恢复');
  } catch (error) { notice(error.message); }
});

for (const type of ['down', 'move', 'up', 'cancel']) {
  frame.addEventListener('pointer' + type, event => {
    if (!qqAccount || (!qqDrag && type !== 'down')) return;
    if (type === 'move' && Date.now() - qqLastMove < 50) return;
    qqLastMove = Date.now();
    if (type === 'down') { qqDrag = true; frame.setPointerCapture(event.pointerId); }
    if (type === 'up' || type === 'cancel') qqDrag = false;
    const bounds = frame.getBoundingClientRect();
    const coordinates = { type: type === 'cancel' ? 'up' : type,
      x: (event.clientX - bounds.left) * 400 / bounds.width,
      y: (event.clientY - bounds.top) * 720 / bounds.height };
    const endpoint = qqPath('verification/pointer');
    qqPointerChain = qqPointerChain.then(() => api(endpoint, coordinates)).catch(error => notice(error.message));
  });
}

function listControl(list, text, label, action) {
  const item = document.createElement('li');
  const description = document.createElement('span');
  description.textContent = text;
  if (label) {
    const button = document.createElement('button');
    button.type = 'button';
    button.textContent = label;
    button.addEventListener('click', () => action().catch(error => notice(error.message)));
    item.append(description, button);
  } else {
    item.append(description);
  }
  list.append(item);
}

async function loadOperations() {
  const [tokens, users, monitor, webhook, logs] = await Promise.all([
    api('/admin/tokens'), api('/admin/users'), api('/admin/monitor'), api('/admin/webhook'), api('/admin/monitor/logs?limit=12')
  ]);
  $('tokens').replaceChildren();
  for (const token of tokens.data) {
    listControl($('tokens'), token.name, '撤销', async () => {
      if (!confirm('撤销此 Token？')) return;
      await api('/admin/tokens/' + encodeURIComponent(token.id), {}, 'DELETE');
      await loadOperations();
    });
  }
  $('users').replaceChildren();
  for (const user of users.data) {
    listControl($('users'), user.username + ' · ' + (user.role === 'admin' ? '管理员' : '普通用户'), '删除', async () => {
      if (!confirm('删除此后台用户？')) return;
      await api('/admin/users/' + encodeURIComponent(user.username), {}, 'DELETE');
      await loadOperations();
    });
  }
  $('monitor-form').elements.enabled.checked = monitor.data.enabled;
  $('monitor-form').elements.interval.value = monitor.data.interval;
  $('webhook-form').elements.enabled.checked = webhook.data.enabled;
  $('webhook-form').elements.url.value = webhook.data.url;
  $('webhook-form').elements.contentType.value = webhook.data.contentType;
  $('webhook-form').elements.headers.value = JSON.stringify(webhook.data.headers || {});
  $('webhook-form').elements.bodyTemplate.value = webhook.data.bodyTemplate || '';
  $('monitor-logs').replaceChildren();
  for (const entry of logs.data) {
    const info = [new Date(entry.timestamp).toLocaleString(), entry.type, entry.platform, entry.note].filter(Boolean);
    listControl($('monitor-logs'), info.join(' · '));
  }
}

$('token-form').addEventListener('submit', async event => {
  event.preventDefault();
  const fields = new FormData(event.currentTarget);
  try {
    const result = await api('/admin/tokens', { name: fields.get('name') });
    $('new-token').textContent = result.data.token;
    $('new-token').hidden = false;
    $('token-form').reset();
    await loadOperations();
  } catch (error) { notice(error.message); }
});

$('user-form').addEventListener('submit', async event => {
  event.preventDefault();
  try {
    await api('/admin/users', Object.fromEntries(new FormData(event.currentTarget)));
    $('user-form').reset();
    await loadOperations();
  } catch (error) { notice(error.message); }
});

$('monitor-form').addEventListener('submit', async event => {
  event.preventDefault();
  const form = event.currentTarget;
  try {
    await api('/admin/monitor', { enabled: form.elements.enabled.checked, interval: Number(form.elements.interval.value) }, 'PUT');
    notice('监测设置已保存');
  } catch (error) { notice(error.message); }
});

$('monitor-now').addEventListener('click', async event => {
  event.target.disabled = true;
  try {
    await api('/admin/monitor/check', {});
    await loadOperations();
    notice('账号检查已完成');
  } catch (error) { notice(error.message); }
  finally { event.target.disabled = false; }
});

$('webhook-form').addEventListener('submit', async event => {
  event.preventDefault();
  const form = event.currentTarget;
  const fields = Object.fromEntries(new FormData(form));
  try {
    await api('/admin/webhook', { ...fields, enabled: form.elements.enabled.checked,
      headers: JSON.parse(fields.headers || '{}') }, 'PUT');
    notice('Webhook 设置已保存');
  } catch (error) { notice(error.message); }
});

$('webhook-test').addEventListener('click', async event => {
  event.target.disabled = true;
  try { await api('/admin/webhook/test', {}); notice('Webhook 测试消息已发送'); }
  catch (error) { notice(error.message); }
  finally { event.target.disabled = false; }
});
