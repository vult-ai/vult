package access

// particlesScript draws the drifting particles behind the sign-in and account
// pages. It is a plain-script port of Magic UI's Particles component, with the
// same settings as the hosted sign-in: 120 particles that fade in, drift
// slowly and lean toward the pointer. With reduced motion they stay still.
const particlesScript = `/*
 * Based on Magic UI Particles, https://magicui.design/docs/components/particles
 * MIT License. Copyright (c) Magic UI
 *
 * Permission is hereby granted, free of charge, to any person obtaining a copy
 * of this software and associated documentation files (the "Software"), to deal
 * in the Software without restriction, including without limitation the rights
 * to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
 * copies of the Software, and to permit persons to whom the Software is
 * furnished to do so, subject to the following conditions:
 *
 * The above copyright notice and this permission notice shall be included in
 * all copies or substantial portions of the Software.
 *
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
 * IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
 * FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
 * AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
 * LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
 * OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
 * SOFTWARE.
 */
(function () {
  "use strict";
  var quantity = 120, staticity = 50, ease = 20, baseSize = 0.4, rgb = "102, 102, 102";
  function start() {
    var canvas = document.createElement("canvas");
    canvas.className = "particles";
    canvas.setAttribute("aria-hidden", "true");
    document.body.prepend(canvas);
    var context = canvas.getContext("2d");
    if (!context) return;
    var motion = window.matchMedia("(prefers-reduced-motion: reduce)");
    var still = motion.matches, w = 0, h = 0, dpr = 1, circles = [], frame = 0;
    var mouse = { x: 0, y: 0 };
    function circle() {
      return {
        x: Math.floor(Math.random() * w), y: Math.floor(Math.random() * h),
        tx: 0, ty: 0, size: Math.floor(Math.random() * 2) + baseSize,
        alpha: 0, target: parseFloat((Math.random() * 0.6 + 0.1).toFixed(1)),
        dx: (Math.random() - 0.5) * 0.1, dy: (Math.random() - 0.5) * 0.1,
        magnetism: 0.1 + Math.random() * 4
      };
    }
    function draw(c) {
      context.setTransform(dpr, 0, 0, dpr, 0, 0);
      context.translate(c.tx, c.ty);
      context.beginPath();
      context.arc(c.x, c.y, c.size, 0, 2 * Math.PI);
      context.fillStyle = "rgba(" + rgb + ", " + c.alpha + ")";
      context.fill();
    }
    function resize() {
      dpr = window.devicePixelRatio || 1;
      w = window.innerWidth; h = window.innerHeight;
      canvas.width = w * dpr; canvas.height = h * dpr;
      canvas.style.width = w + "px"; canvas.style.height = h + "px";
      circles = [];
      for (var i = 0; i < quantity; i++) circles.push(circle());
    }
    function edgeFade(c) {
      var edge = Math.min(c.x + c.tx - c.size, w - c.x - c.tx - c.size, c.y + c.ty - c.size, h - c.y - c.ty - c.size);
      return Math.max(0, edge / 20);
    }
    function animate() {
      context.setTransform(1, 0, 0, 1, 0, 0);
      context.clearRect(0, 0, canvas.width, canvas.height);
      for (var i = 0; i < circles.length; i++) {
        var c = circles[i], fade = edgeFade(c);
        if (still) {
          c.alpha = c.target * Math.min(1, fade);
        } else {
          c.alpha = fade > 1 ? Math.min(c.target, c.alpha + 0.02) : c.target * fade;
          c.x += c.dx; c.y += c.dy;
          c.tx += (mouse.x / (staticity / c.magnetism) - c.tx) / ease;
          c.ty += (mouse.y / (staticity / c.magnetism) - c.ty) / ease;
        }
        draw(c);
        if (c.x < -c.size || c.x > w + c.size || c.y < -c.size || c.y > h + c.size) circles[i] = circle();
      }
      frame = !still && !document.hidden ? window.requestAnimationFrame(animate) : 0;
    }
    function restart() {
      if (frame) window.cancelAnimationFrame(frame);
      frame = 0;
      still = motion.matches;
      if (!document.hidden) animate();
    }
    var resizing = 0;
    window.addEventListener("resize", function () {
      clearTimeout(resizing);
      resizing = setTimeout(function () { resize(); restart(); }, 200);
    });
    window.addEventListener("mousemove", function (event) {
      var x = event.clientX - w / 2, y = event.clientY - h / 2;
      if (Math.abs(x) < w / 2 && Math.abs(y) < h / 2) { mouse.x = x; mouse.y = y; }
    });
    document.addEventListener("visibilitychange", restart);
    if (motion.addEventListener) motion.addEventListener("change", restart);
    resize();
    restart();
  }
  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", start);
  else start();
})();
`
