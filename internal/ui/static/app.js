// Copy buttons: <button data-copy="...">. The only script besides htmx + idiomorph.
document.addEventListener("click", function (e) {
  var b = e.target.closest("button[data-copy]");
  if (!b || !navigator.clipboard) return;
  navigator.clipboard.writeText(b.dataset.copy).then(function () {
    var t = b.textContent;
    b.textContent = "✓";
    setTimeout(function () { b.textContent = t; }, 1200);
  });
});
