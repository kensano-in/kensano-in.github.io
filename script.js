const target=new Date();target.setDate(target.getDate()+30);
const d=document.getElementById('d'),h=document.getElementById('h'),
m=document.getElementById('m'),s=document.getElementById('s');
const pad=n=>String(n).padStart(2,'0');
function tick(){
 let diff=Math.max(0,target-new Date());
 const days=Math.floor(diff/86400000);diff%=86400000;
 const hours=Math.floor(diff/3600000);diff%=3600000;
 const mins=Math.floor(diff/60000);
 const secs=Math.floor((diff%60000)/1000);
 d.textContent=pad(days);h.textContent=pad(hours);
 m.textContent=pad(mins);s.textContent=pad(secs);
}
tick();setInterval(tick,1000);
