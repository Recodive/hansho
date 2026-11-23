// Progressive enhancement only: every page works without this file. It shows the copy buttons, selects a
// read-only value (a pair link, a new key, an API call) when it gets focus, and explains the chosen level.
for (const input of document.querySelectorAll("input[data-select], textarea[data-select]")) {
  input.addEventListener("focus", () => input.select());
}
// A level select explains the chosen level under itself; without this file the page shows the explanation it was sent with.
for (const select of document.querySelectorAll("select[data-help]")) {
  const out = document.getElementById(select.dataset.help);
  if (!out) continue;
  const show = () => { out.textContent = select.selectedOptions[0]?.dataset.help ?? ""; };
  select.addEventListener("change", show);
  show();
}
if (navigator.clipboard) {
  for (const btn of document.querySelectorAll("button[data-copy]")) {
    const input = document.getElementById(btn.dataset.copy);
    if (!input) continue;
    const label = btn.textContent;
    btn.hidden = false;
    btn.addEventListener("click", async () => {
      try {
        await navigator.clipboard.writeText(input.value);
        btn.textContent = "Copied";
        setTimeout(() => { btn.textContent = label; }, 2000);
      } catch {
        input.focus();
      }
    });
  }
}
