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

  // Announces completion into the banner's live region, then reloads.
  //
  // The reload on its own is silent to assistive technology: the page simply
  // gets replaced, with nothing to say a result arrived. Writing into the
  // #in-flight-banner element (role="status", so polite-live) gives a screen
  // reader something to read out, and the short delay exists purely to let
  // it do so — a reload fired in the same tick destroys the announcement
  // before it is ever queued. Sighted users see the same text for the same
  // moment, which is honest about what is happening rather than a blank
  // pause.
  function finishAndReload() {
    var message = document.getElementById("in-flight-message");
    if (!message) {
      window.location.reload();
      return;
    }
    message.textContent = "Check complete. Loading the results…";
    setTimeout(function () {
      window.location.reload();
    }, 1000);
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
          finishAndReload();
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
