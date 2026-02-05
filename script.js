
const verlyn = document.getElementById('verlyn');
const verlynLink = document.getElementById('verlynLink');
const shinkenLink = document.getElementById('shinkenLink');
const whisper = document.getElementById('whisper');

verlyn.addEventListener('click', () => {
  window.open('https://instagram.com/verlyn.in', '_blank');
});

verlynLink.addEventListener('click', () => {
  window.open('https://instagram.com/verlyn.in', '_blank');
});

shinkenLink.addEventListener('click', () => {
  window.open('https://shinken.in', '_blank');
});

let pressTimer;
document.body.addEventListener('touchstart', startPress);
document.body.addEventListener('mousedown', startPress);

document.body.addEventListener('touchend', cancelPress);
document.body.addEventListener('mouseup', cancelPress);

function startPress() {
  pressTimer = setTimeout(() => {
    whisper.classList.add('show');
    setTimeout(() => whisper.classList.remove('show'), 4000);
  }, 2000);
}

function cancelPress() {
  clearTimeout(pressTimer);
}
