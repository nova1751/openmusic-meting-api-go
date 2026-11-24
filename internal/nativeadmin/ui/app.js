let csrf = '';
let timer;
let qrRun = 0;
const $ = id => document.getElementById(id);
const notice = text => { $('notice').textContent = text; };

async function api(path, body) {
  const options = { credentials: 'same-origin', headers: {} };
  if (body !== undefined) {
    options.method = 'POST';
    options.headers['Content-Type'] = 'application/json';
    if (csrf) options.headers['X-CSRF-Token'] = csrf;
    options.body = JSON.stringify(body);
  }
  const response = await fetch('/admin/api/' + path, options);
  const result = await response.json();
  if (!response.ok) throw new Error(result.error || '请求失败');
  return result;
}

function showLogin() {
  clearTimeout(timer);
  qrRun++;
  csrf = '';
  $('dashboard').hidden = true;
  $('login').hidden = false;
  $('qr').hidden = true;
}

async function loadAccounts() {
  const result = await api('accounts');
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
    item.append(description, button);
    list.append(item);
  }
  if (!result.accounts.length) list.textContent = '暂无账号';
}

async function showDashboard() {
  $('login').hidden = true;
  $('dashboard').hidden = false;
  notice('');
  await Promise.all([loadAccounts(), loadTwoFactor()]);
}

$('login-form').addEventListener('submit', async event => {
  event.preventDefault();
  const form = new FormData(event.currentTarget);
  try {
    const result = await api('login', { username: form.get('username'), password: form.get('password'), code: form.get('code') || '' });
    if (result.require2FA) { $('code-label').hidden = false; notice('请输入双重验证码'); return; }
    csrf = result.csrf;
    event.currentTarget.reset();
    await showDashboard();
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
    } else if (result.status === 'expired') {
      $('qr-status').textContent = '二维码已过期，请重新生成。';
    } else {
      $('qr-status').textContent = result.status === 'scanned' ? '已扫码，请在手机上确认。' : '请使用网易云音乐 App 扫码。';
      timer = setTimeout(() => poll(run), 2500);
    }
  } catch (error) { if (run === qrRun) $('qr-status').textContent = error.message; }
}

$('create-qr').addEventListener('click', async () => {
  clearTimeout(timer);
  const run = ++qrRun;
  $('create-qr').disabled = true;
  try {
    await api('qr/create', {});
    $('qr').hidden = false;
    $('qr-status').textContent = '请使用网易云音乐 App 扫码。';
    $('qr-image').src = '/admin/api/qr/image?t=' + Date.now();
    timer = setTimeout(() => poll(run), 2500);
  } catch (error) { notice(error.message); }
  finally { $('create-qr').disabled = false; }
});

api('session').then(result => { csrf = result.csrf; return showDashboard(); }).catch(showLogin);
