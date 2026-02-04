const canvas = document.getElementById('void');
const ctx = canvas.getContext('2d');

let w, h;
function resize() {
  w = canvas.width = window.innerWidth;
  h = canvas.height = window.innerHeight;
}
window.addEventListener('resize', resize);
resize();

const points = Array.from({ length: 140 }, () => ({
  x: Math.random() * w,
  y: Math.random() * h,
  z: Math.random() * w
}));

function animate() {
  ctx.clearRect(0, 0, w, h);
  for (let p of points) {
    p.z -= 1.2;
    if (p.z <= 0) p.z = w;
    const k = 120 / p.z;
    const px = p.x * k + w / 2;
    const py = p.y * k + h / 2;
    if (px >= 0 && px <= w && py >= 0 && py <= h) {
      const size = (1 - p.z / w) * 2;
      ctx.fillStyle = 'rgba(255,255,255,0.7)';
      ctx.fillRect(px, py, size, size);
    }
  }
  requestAnimationFrame(animate);
}
animate();
