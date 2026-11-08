// The data page re-fetches itself once /ui/app.js has written the tz
// cookie, to show its times in the browser's zone.
document.addEventListener("tz", function () {
  var d = document.getElementById("data");
  if (d && window.htmx) htmx.trigger(d, "tz");
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
