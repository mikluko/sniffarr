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
