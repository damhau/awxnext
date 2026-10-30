// Sets the theme before the first paint, so a dark page never flashes light:
// the user's choice in this browser, else the system's. A file, not an inline
// script, as the page's Content-Security-Policy allows only same-origin
// scripts. The app keeps it in step afterwards (src/lib/theme.ts).
;(function () {
  var choice = 'system'
  try {
    choice = JSON.parse(localStorage.getItem('awxnext:theme') || '"system"')
  } catch (e) {
    /* storage unavailable */
  }
  var dark = choice === 'dark' || (choice !== 'light' && !!window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches)
  document.documentElement.setAttribute('data-theme', dark ? 'dark' : 'light')
})()
