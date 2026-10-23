// Applies the saved theme before first paint (loaded synchronously in <head>).
(function () {
  var pref = 'auto';
  try { pref = localStorage.getItem('fundus.theme') || 'auto'; } catch (e) {}
  var dark = pref === 'dark' || (pref === 'auto' && window.matchMedia('(prefers-color-scheme: dark)').matches);
  document.documentElement.setAttribute('data-theme', dark ? 'dark' : 'light');
})();
