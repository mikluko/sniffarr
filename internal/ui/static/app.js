// The browser's time zone goes to the tz cookie, which the server's calendar
// follows; a page rendered before the cookie existed re-fetches its data.
document.addEventListener("DOMContentLoaded", function () {
  var tz = Intl.DateTimeFormat().resolvedOptions().timeZone;
  if (!tz || document.cookie.split("; ").indexOf("tz=" + tz) >= 0) return;
  document.cookie = "tz=" + tz + "; path=/; max-age=31536000; SameSite=Lax" + (location.protocol === "https:" ? "; Secure" : "");
  var d = document.getElementById("data");
  if (d && window.htmx) htmx.trigger(d, "tz");
});

// Copy buttons: <button data-copy="...">.
document.addEventListener("click", function (e) {
  var b = e.target.closest("button[data-copy]");
  if (!b || !navigator.clipboard) return;
  navigator.clipboard.writeText(b.dataset.copy).then(function () {
    var t = b.textContent;
    b.textContent = "✓";
    setTimeout(function () { b.textContent = t; }, 1200);
  });
});
