const form = document.querySelector('#create');
const rows = document.querySelector('#rows');
const errorEl = document.querySelector('#error');
const secret = document.querySelector('#secret');

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

function render(keys) {
  rows.replaceChildren();
  for (const key of keys) {
    const tr = document.createElement('tr');
    for (const value of [key.role, key.name || '—', key.prefix, new Date(key.createdAt).toLocaleString()]) {
      const td = document.createElement('td');
      td.textContent = value;
      if (value === key.prefix) td.className = 'mono';
      tr.appendChild(td);
    }
    const action = document.createElement('td');
    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'secondary';
    button.textContent = 'Revoke';
    button.addEventListener('click', async () => {
      button.disabled = true;
      try {
        await api(`/api/admin/keys/${encodeURIComponent(key.id)}`, { method: 'DELETE' });
        await refresh();
      } catch (error) {
        showError(error.message);
        button.disabled = false;
      }
    });
    action.appendChild(button);
    tr.appendChild(action);
    rows.appendChild(tr);
  }
}

async function refresh() {
  const keys = await api('/api/admin/keys');
  if (keys) render(keys);
}

form.addEventListener('submit', async (event) => {
  event.preventDefault();
  const role = event.submitter?.dataset.role;
  if (!role) return;
  showError('');
  secret.hidden = true;
  try {
    const created = await api('/api/admin/keys', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ role, name: new FormData(form).get('name') }),
    });
    if (!created) return;
    secret.hidden = false;
    secret.textContent = created.token;
    form.elements.name.value = '';
    await refresh();
  } catch (error) {
    showError(error.message);
  }
});

refresh().catch((error) => showError(error.message));
