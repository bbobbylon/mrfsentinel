// Polls a run's status endpoint while it's still pending/running, and
// reloads the page once it's finished — see run.html, which only includes
// this script at all while a run is in flight. Deliberately plain: no
// framework, no build step, just the DOM APIs every browser already has.
(function () {
  "use strict";

  var script = document.currentScript;
  var statusURL = script && script.getAttribute("data-run-status-url");
  if (!statusURL) {
    return;
  }

  function poll() {
    fetch(statusURL, { credentials: "same-origin" })
      .then(function (res) {
        if (!res.ok) {
          throw new Error("status check failed: " + res.status);
        }
        return res.json();
      })
      .then(function (data) {
        if (data.status === "pending" || data.status === "running") {
          setTimeout(poll, 3000);
        } else {
          window.location.reload();
        }
      })
      .catch(function () {
        // A transient network hiccup shouldn't stop polling — the whole
        // reason this script is running is that someone is watching this
        // page for a slow, multi-gigabyte file to finish.
        setTimeout(poll, 5000);
      });
  }

  setTimeout(poll, 3000);
})();
