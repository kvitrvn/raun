// Behavior of the web interface. Every page works without it; it adds the
// theme toggle, opens and closes dialogs where the browser lacks invoker
// commands, and places and drives dropdown menus. Listeners are delegated
// to the document, so content swapped in by HTMX keeps working. No eval, no
// inline handler, no markup built from strings.
(function () {
  "use strict";

  var root = document.documentElement;
  var invokers = typeof HTMLButtonElement !== "undefined" && "command" in HTMLButtonElement.prototype;

  function closest(target, selector) {
    return target instanceof Element ? target.closest(selector) : null;
  }

  document.addEventListener("click", function (event) {
    var toggle = closest(event.target, "[data-theme-toggle]");
    if (toggle) {
      var next = root.getAttribute("data-theme") === "dark" ? "light" : "dark";
      root.setAttribute("data-theme", next);
      try {
        window.localStorage.setItem("raun-theme", next);
      } catch (e) {
        // The choice lasts for this page only.
      }
      return;
    }
    if (invokers) {
      return;
    }
    var opener = closest(event.target, "[data-dialog-open]");
    if (opener) {
      var dialog = document.getElementById(opener.getAttribute("data-dialog-open"));
      if (dialog instanceof HTMLDialogElement && !dialog.open) {
        event.preventDefault();
        dialog.showModal();
      }
      return;
    }
    var closer = closest(event.target, "[data-dialog-close]");
    if (closer) {
      var owner = document.getElementById(closer.getAttribute("data-dialog-close"));
      if (owner instanceof HTMLDialogElement && owner.open) {
        event.preventDefault();
        owner.close();
      }
    }
  });

  function menuItems(menu) {
    return Array.prototype.slice.call(menu.querySelectorAll('[role="menuitem"]:not([aria-disabled="true"])'));
  }

  function triggerOf(menu) {
    return document.querySelector('[popovertarget="' + CSS.escape(menu.id) + '"]');
  }

  // Places an open menu under its trigger, or above it when there is more
  // room there, inside the viewport; the stylesheet bounds its height, and
  // it scrolls when neither side holds it. Setting style properties from
  // script is allowed by the Content Security Policy; style attributes in
  // markup are not.
  function place(menu, trigger) {
    var gap = 4, edge = 8;
    var box = trigger.getBoundingClientRect();
    menu.style.position = "fixed";
    menu.style.margin = "0";
    var below = window.innerHeight - box.bottom - gap - edge;
    var above = box.top - gap - edge;
    var height = menu.offsetHeight;
    if (height <= below || below >= above) {
      menu.style.maxHeight = Math.max(below, 0) + "px";
      menu.style.top = Math.round(box.bottom + gap) + "px";
    } else {
      menu.style.maxHeight = Math.max(above, 0) + "px";
      menu.style.top = Math.round(Math.max(edge, box.top - gap - Math.min(height, above))) + "px";
    }
    var left = Math.min(box.left, window.innerWidth - menu.offsetWidth - edge);
    menu.style.left = Math.round(Math.max(edge, left)) + "px";
  }

  // The toggle event does not bubble: it is caught on the way down.
  document.addEventListener("toggle", function (event) {
    var menu = event.target;
    if (!(menu instanceof HTMLElement) || menu.getAttribute("data-slot") !== "dropdown-menu-content") {
      return;
    }
    var trigger = triggerOf(menu);
    var open = event.newState === "open";
    if (trigger) {
      trigger.setAttribute("aria-expanded", open ? "true" : "false");
    }
    if (open) {
      if (trigger) {
        place(menu, trigger);
      }
      var items = menuItems(menu);
      if (items.length > 0) {
        items[0].focus();
      }
    }
  }, true);

  document.addEventListener("keydown", function (event) {
    var menu = closest(event.target, '[data-slot="dropdown-menu-content"]');
    if (!menu) {
      return;
    }
    var items = menuItems(menu);
    if (items.length === 0) {
      return;
    }
    var index = items.indexOf(document.activeElement);
    var next = -1;
    switch (event.key) {
      case "ArrowDown":
        next = (index + 1) % items.length;
        break;
      case "ArrowUp":
        next = (index - 1 + items.length) % items.length;
        break;
      case "Home":
        next = 0;
        break;
      case "End":
        next = items.length - 1;
        break;
    }
    if (next >= 0) {
      event.preventDefault();
      items[next].focus();
    }
  });
})();
