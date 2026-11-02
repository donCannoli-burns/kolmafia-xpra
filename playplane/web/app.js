// Live-editable HTML/CSS/JS: changes take effect when the play plane is reloaded.
const buttons = [...document.querySelectorAll('button[data-view]')];
for (const button of buttons) {
  button.addEventListener('click', () => {
    const mode = button.dataset.view;
    document.body.dataset.mode = mode;
    for (const other of buttons) other.setAttribute('aria-pressed', String(other === button));
  });
}
