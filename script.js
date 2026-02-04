const reveal = document.getElementById('reveal');

document.querySelector('[data-action="instagram"]').onclick = () => {
  window.open('https://instagram.com/verlyn.in', '_blank');
};

document.querySelector('[data-action="shinken"]').onclick = () => {
  window.open('https://shinken.in', '_blank');
};

document.querySelector('[data-person="shin"]').onclick = () => {
  reveal.textContent = 'Founder. Architect of silence and structure.';
};

document.querySelector('[data-person="ken"]').onclick = () => {
  reveal.textContent = 'Co-founder. Keeper of clarity and restraint.';
};
