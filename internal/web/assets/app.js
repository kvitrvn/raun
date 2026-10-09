// Keep keyboard and screen-reader navigation aligned with the replaced page.
function announcePage() {
  const main = document.getElementById('main');
  if (!main) return;
  const error = document.getElementById('error-summary');
  (error || main).focus({ preventScroll: !error });
  const heading = main.querySelector('h1');
  document.getElementById('navigation-status').textContent = heading ? heading.textContent : '';
}
document.addEventListener('htmx:afterSettle', announcePage);
document.addEventListener('htmx:historyRestore', announcePage);
document.addEventListener('htmx:sendError', () => {
  document.getElementById('navigation-status').textContent = 'Cannot reach Raun. Check that the server is running, then reload the page.';
});
// A browser back-forward cache must not bypass a fresh store read.
window.addEventListener('pageshow', (event) => {
  if (event.persisted) window.location.reload();
});
