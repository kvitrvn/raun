// Sets the theme before the page paints: the one the person chose, else the
// one of the system. Loaded in the head, without defer, so that no page
// flashes in the wrong theme. Storage may be unavailable; the system then
// decides.
(function () {
  "use strict";
  var theme = null;
  try {
    theme = window.localStorage.getItem("raun-theme");
  } catch (e) {
    theme = null;
  }
  if (theme !== "light" && theme !== "dark") {
    var dark = window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches;
    theme = dark ? "dark" : "light";
  }
  document.documentElement.setAttribute("data-theme", theme);
})();
