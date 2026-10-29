document.querySelector('#logout')?.addEventListener('click', async () => {
  await fetch('/api/admin/logout', { method: 'POST' });
  window.location = '/login';
});
