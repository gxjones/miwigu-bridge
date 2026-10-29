const errorEl = document.querySelector('#error');
const statusEl = document.querySelector('#status');
const launch = document.querySelector('#launch');
const pendingKey = 'helix-launch';

function showError(message) {
  errorEl.hidden = !message;
  errorEl.textContent = message || '';
}

async function api(path, options) {
  const response = await fetch(path, options);
  if (response.status === 401) {
    window.location = '/login';
    return null;
  }
  const body = await response.json();
  if (!response.ok) throw new Error(body.error || 'Request failed');
  return body;
}

function delay(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function waiting() {
  launch.hidden = true;
  statusEl.hidden = false;
  statusEl.textContent = 'Starting…';
}

function ready() {
  launch.hidden = false;
  launch.disabled = false;
  statusEl.hidden = true;
}

function stillWaiting(row) {
  return row.state === 'PENDING' || row.state === 'SUSPENDING' || (row.state === 'RUNNING' && !row.viewer);
}

async function follow(id) {
  sessionStorage.setItem(pendingKey, id);
  waiting();
  for (;;) {
    let row;
    try {
      row = await api('/api/admin/microvms/' + encodeURIComponent(id));
    } catch (err) {
      sessionStorage.removeItem(pendingKey);
      showError(err.message);
      ready();
      return;
    }
    if (!row) return;
    if (row.viewer) {
      sessionStorage.removeItem(pendingKey);
      window.location = row.viewer;
      return;
    }
    if (row.desktopError || !stillWaiting(row)) {
      sessionStorage.removeItem(pendingKey);
      showError(row.desktopError || row.state || 'The desktop did not start.');
      ready();
      return;
    }
    await delay(3000);
  }
}

launch?.addEventListener('click', async () => {
  launch.disabled = true;
  showError('');
  try {
    const row = await api('/api/admin/launch', { method: 'POST' });
    if (!row) return;
    if (row.viewer) {
      window.location = row.viewer;
      return;
    }
    if (row.desktopError) {
      showError(row.desktopError);
      launch.disabled = false;
      return;
    }
    await follow(row.microvmId);
  } catch (err) {
    showError(err.message);
    launch.disabled = false;
  }
});

const saved = sessionStorage.getItem(pendingKey);
if (saved) follow(saved);
