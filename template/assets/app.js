"use strict";

(() => {
  const progress = document.getElementById("progress");
  const status = document.getElementById("status");
  const refresh = document.getElementById("refresh");
  const chart = document.getElementById("chart");
  const context = chart.getContext("2d");
  const palette = ["#779989", "#adc6ad", "#c0d69b", "#e5dcaf"];
  let revision = 0;

  function observations(seed) {
    return Array.from({length: 16}, (_, index) => {
      const wave = Math.sin((index + seed) * 0.72) * 18;
      const trend = 22 + index * 3;
      return Math.max(8, Math.min(112, trend + wave));
    });
  }

  function draw(values) {
    context.clearRect(0, 0, chart.width, chart.height);
    const width = chart.width / values.length;
    values.forEach((value, index) => {
      context.fillStyle = palette[(index + revision) % palette.length];
      context.fillRect(
        index * width + 1,
        chart.height - value,
        Math.max(2, width - 3),
        value
      );
    });
  }

  async function render() {
    refresh.disabled = true;
    status.textContent = "Refreshing observations...";
    progress.value = 0;
    const values = observations(revision++);
    for (let index = 0; index < values.length; index++) {
      progress.value = index + 1;
      draw(values.slice(0, index + 1));
      await new Promise(resolve => requestAnimationFrame(resolve));
    }
    status.textContent = "Archive synchronized.";
    refresh.disabled = false;
  }

  refresh.addEventListener("click", render);
  window.addEventListener("load", render, {once: true});
})();
