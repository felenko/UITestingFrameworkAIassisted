// Shared report behaviour: collapsible cases, image lightbox, expected/actual
// opacity overlay. Used by report.html and (optionally) the GUI results view.
(function () {
  function ready(fn) {
    if (document.readyState !== "loading") fn();
    else document.addEventListener("DOMContentLoaded", fn);
  }

  ready(function () {
    // Collapsible case rows.
    document.querySelectorAll(".case-head").forEach(function (head) {
      head.addEventListener("click", function () {
        head.parentElement.classList.toggle("open");
      });
    });

    // Lightbox.
    var lb = document.createElement("div");
    lb.className = "lightbox";
    var lbImg = document.createElement("img");
    lb.appendChild(lbImg);
    document.body.appendChild(lb);
    lb.addEventListener("click", function () { lb.classList.remove("show"); });
    document.addEventListener("keydown", function (e) {
      if (e.key === "Escape") lb.classList.remove("show");
    });
    document.addEventListener("click", function (e) {
      var img = e.target.closest(".pane img, .step-shots img");
      if (img) {
        lbImg.src = img.src;
        lb.classList.add("show");
      }
    });

    // Expected/actual opacity overlay sliders.
    document.querySelectorAll(".aid input[type=range]").forEach(function (slider) {
      slider.addEventListener("input", function () {
        var card = slider.closest(".assert");
        var expected = card.querySelector(".pane-expected img");
        if (expected) expected.style.opacity = slider.value / 100;
      });
    });
  });
})();
