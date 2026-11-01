export function followSystemTheme() {
  const dark = window.matchMedia("(prefers-color-scheme: dark)");
  const apply = () => {
    document.documentElement.dataset.theme = dark.matches ? "dark" : "light";
  };
  apply();
  dark.addEventListener("change", apply);
}
