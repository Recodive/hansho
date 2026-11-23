// Progressive enhancement only: every page works without this file. It shows the copy buttons and selects a
// read-only value (a pair link, a new key) when it gets focus.
for (const input of document.querySelectorAll("input[data-select]")) {
  input.addEventListener("focus", () => input.select());
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
