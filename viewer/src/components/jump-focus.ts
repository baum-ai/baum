/** Center a jump in a fixed, short duration, independent of its distance. */
export function centerJump(target: HTMLElement, reduced: boolean): () => void {
  const containers: HTMLElement[] = [];
  for (let parent = target.parentElement; parent; parent = parent.parentElement) {
    if (parent.scrollHeight > parent.clientHeight) containers.push(parent);
  }
  const root = document.scrollingElement as HTMLElement | null;
  if (root && !containers.includes(root)) containers.push(root);
  const before = containers.map((element) => element.scrollTop);
  target.scrollIntoView({ block: "center", behavior: "instant" });
  if (reduced) return () => {};
  const moves = containers
    .map((element, index) => ({ element, from: before[index]!, to: element.scrollTop }))
    .filter(({ from, to }) => from !== to);
  if (!moves.length) return () => {};
  for (const move of moves) move.element.scrollTo({ top: move.from, behavior: "instant" });
  const started = performance.now();
  let frame = 0;
  const step = (now: number) => {
    const progress = Math.min(1, (now - started) / 400);
    const eased = 1 - (1 - progress) ** 3;
    for (const move of moves)
      move.element.scrollTo({
        top: move.from + (move.to - move.from) * eased,
        behavior: "instant",
      });
    if (progress < 1) frame = requestAnimationFrame(step);
  };
  frame = requestAnimationFrame(step);
  return () => cancelAnimationFrame(frame);
}

/** Paint the visible target, using a resolved color rather than a CSS variable keyframe. */
export function flashJump(target: HTMLElement, reduced: boolean): void {
  const previous = target.style.backgroundColor;
  target.style.backgroundColor =
    "color-mix(in srgb, var(--action-accent) 22%, var(--surface-card))";
  const color = getComputedStyle(target).backgroundColor;
  target.style.backgroundColor = previous;
  target.animate?.(
    [{ backgroundColor: color }, { backgroundColor: color, offset: reduced ? 0.99 : 0.55 }, {}],
    { duration: 2000, easing: "ease-out" },
  );
}
