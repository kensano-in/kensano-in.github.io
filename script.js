const reveal = document.getElementById('reveal');
const tone = document.getElementById('tone');

function ritualSound(){
  tone.currentTime = 0;
  tone.play().catch(()=>{});
}

document.querySelectorAll('.portal').forEach(el=>{
  el.addEventListener('click',()=>{
    ritualSound();
    setTimeout(()=>{
      if(el.dataset.go==='ig'){
        window.location.href='https://instagram.com/verlyn.in';
      }else{
        window.location.href='https://shinken.in';
      }
    },450);
  });
});

document.querySelector('[data-person="shin"]').onclick=()=>{
  ritualSound();
  reveal.textContent='Founder — language, vision, authorship.';
  reveal.classList.add('show');
};
document.querySelector('[data-person="ken"]').onclick=()=>{
  ritualSound();
  reveal.textContent='Co-founder — systems, structure, execution.';
  reveal.classList.add('show');
};
