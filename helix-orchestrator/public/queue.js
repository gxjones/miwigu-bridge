const rows = document.querySelector('#rows');
const empty = document.querySelector('#empty');
const errorEl = document.querySelector('#error');

function when(value) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '';
  return date.toLocaleString();
}

function render(items) {
  rows.replaceChildren();
  empty.hidden = items.length > 0;
  for (const item of items) {
    const tr = document.createElement('tr');
    const result = document.createElement('td');
    result.className = 'mono';
    const text = JSON.stringify(item.result);
    result.textContent = text.length > 80 ? `${text.slice(0, 80)}…` : text;
    result.title = text;
    const cells = [item.os, item.uid, item.status, when(item.updatedAt)];
    for (const [index, value] of cells.entries()) {
      const td = document.createElement('td');
      if (index === 1) td.className = 'mono';
      if (index === 2) td.className = `status-${item.status}`;
      td.textContent = value;
      tr.appendChild(td);
    }
    tr.appendChild(result);
    const viewer = document.createElement('td');
    if (item.viewer) {
      const link = document.createElement('a');
      link.href = item.viewer;
      link.target = '_blank';
      link.rel = 'noopener';
      link.textContent = 'View';
      viewer.appendChild(link);
    }
    tr.appendChild(viewer);
    rows.appendChild(tr);
  }
}

async function refresh() {
  try {
    const response = await fetch('/api/admin/queue');
    if (response.status === 401) {
      window.location = '/login';
      return;
    }
    const body = await response.json();
    if (!response.ok) throw new Error(body.error || 'Could not load the queue');
    errorEl.hidden = true;
    render(body);
  } catch (error) {
    errorEl.hidden = false;
    errorEl.textContent = error.message;
  }
}

refresh();
setInterval(refresh, 2000);
