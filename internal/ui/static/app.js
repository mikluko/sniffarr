// The browser's time zone goes to the tz cookie, which the server's calendar
// follows; a page rendered before the cookie existed re-fetches its data.
document.addEventListener("DOMContentLoaded", function () {
  var tz = Intl.DateTimeFormat().resolvedOptions().timeZone;
  if (!tz || document.cookie.split("; ").indexOf("tz=" + tz) >= 0) return;
  document.cookie = "tz=" + tz + "; path=/; max-age=31536000; SameSite=Lax" + (location.protocol === "https:" ? "; Secure" : "");
  var d = document.getElementById("data");
  if (d && window.htmx) htmx.trigger(d, "tz");
});

// Copy buttons: <button data-copy="..." data-done="..."> inside a .tip, whose
// tooltip shows data-done for a moment once the value is copied.
document.addEventListener("click", function (e) {
  var b = e.target.closest("button[data-copy]");
  if (!b || !navigator.clipboard) return;
  navigator.clipboard.writeText(b.dataset.copy).then(function () {
    var w = b.closest(".tip"), tc = w && w.querySelector(".tipb");
    if (!tc) return;
    if (!tc.dataset.label) tc.dataset.label = tc.textContent;
    tc.textContent = b.dataset.done;
    w.classList.add("open");
    clearTimeout(w.copyTimer);
    w.copyTimer = setTimeout(function () {
      tc.textContent = tc.dataset.label;
      w.classList.remove("open");
    }, 1200);
  });
});

// Disclosure buttons: <button data-toggle aria-controls="id" aria-expanded>
// show the element id while pressed, by its class "open"; the state outlives
// an htmx swap that renders both again.
var disclosed = {};
function disclose(b, open) {
  var el = document.getElementById(b.getAttribute("aria-controls"));
  if (!el) return;
  el.classList.toggle("open", open);
  b.setAttribute("aria-expanded", open ? "true" : "false");
}
document.addEventListener("click", function (e) {
  var b = e.target.closest("button[data-toggle]");
  if (!b) return;
  var id = b.getAttribute("aria-controls");
  disclosed[id] = !disclosed[id];
  disclose(b, disclosed[id]);
});
document.addEventListener("htmx:afterSwap", function () {
  document.querySelectorAll("button[data-toggle]").forEach(function (b) {
    if (disclosed[b.getAttribute("aria-controls")]) disclose(b, true);
  });
});

// keepOpen is the idiomorph config of an outerHTML morph that leaves every
// <details> open or closed as it was: hx-swap="morph:keepOpen".
var keepOpen = {
  morphStyle: "outerHTML",
  callbacks: {
    beforeAttributeUpdated: function (name, el) {
      return !(name === "open" && el.tagName === "DETAILS");
    }
  }
};
