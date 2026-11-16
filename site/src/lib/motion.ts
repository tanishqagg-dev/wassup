import { gsap } from "gsap";
import { ScrollTrigger } from "gsap/ScrollTrigger";
import { SplitText } from "gsap/SplitText";
import { useGSAP } from "@gsap/react";
import Lenis from "lenis";

gsap.registerPlugin(ScrollTrigger, SplitText, useGSAP);

export { gsap, ScrollTrigger, SplitText, useGSAP };

export const RISE = "expo.out";
export const SPRING = "back.out(1.6)";

export function prefersReducedMotion(): boolean {
  return typeof window !== "undefined" && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
}

let lenis: Lenis | null = null;

/** Scroll to an element or a y position, smoothly when Lenis is running. */
export function scrollToTarget(target: HTMLElement | number, offset = 0) {
  if (lenis) {
    lenis.scrollTo(target, { offset });
    return;
  }
  const y = typeof target === "number" ? target : target.getBoundingClientRect().top + window.scrollY + offset;
  window.scrollTo({ top: y });
}

/** Lenis on the GSAP ticker so ScrollTrigger and smooth scroll share one clock. */
export function startSmoothScroll(): () => void {
  const onAnchor = (e: MouseEvent) => {
    const a = (e.target as HTMLElement).closest<HTMLAnchorElement>('a[href^="#"]');
    if (!a) return;
    const id = a.getAttribute("href");
    if (!id || id === "#") return;
    const el = document.querySelector<HTMLElement>(id);
    if (!el) return;
    e.preventDefault();
    scrollToTarget(el, id === "#top" ? 0 : -24);
  };
  document.addEventListener("click", onAnchor);
  if (prefersReducedMotion()) return () => document.removeEventListener("click", onAnchor);
  lenis = new Lenis({ lerp: 0.085, smoothWheel: true });
  lenis.on("scroll", ScrollTrigger.update);
  const tick = (time: number) => lenis?.raf(time * 1000);
  gsap.ticker.add(tick);
  gsap.ticker.lagSmoothing(0);
  return () => {
    document.removeEventListener("click", onAnchor);
    gsap.ticker.remove(tick);
    lenis?.destroy();
    lenis = null;
  };
}

/** One pointer listener for the whole page: a soft light follows the cursor, and .lit surfaces catch it. */
export function startPointerLight(): () => void {
  if (!window.matchMedia("(pointer: fine)").matches) return () => {};
  const root = document.documentElement;
  let raf = 0;
  let last: PointerEvent | null = null;
  let litEl: HTMLElement | null = null;
  const frame = () => {
    raf = 0;
    if (!last) return;
    root.style.setProperty("--mx", `${last.clientX}px`);
    root.style.setProperty("--my", `${last.clientY}px`);
    const el = (last.target as HTMLElement | null)?.closest?.<HTMLElement>(".lit") ?? null;
    if (litEl && litEl !== el) litEl.removeAttribute("data-hot");
    litEl = el;
    if (el) {
      const r = el.getBoundingClientRect();
      el.style.setProperty("--lx", `${last.clientX - r.left}px`);
      el.style.setProperty("--ly", `${last.clientY - r.top}px`);
      el.setAttribute("data-hot", "");
    }
  };
  const onMove = (e: PointerEvent) => {
    last = e;
    root.setAttribute("data-pointer", "");
    if (!raf) raf = requestAnimationFrame(frame);
  };
  const onLeave = () => {
    root.removeAttribute("data-pointer");
    litEl?.removeAttribute("data-hot");
  };
  window.addEventListener("pointermove", onMove, { passive: true });
  document.addEventListener("pointerleave", onLeave);
  return () => {
    window.removeEventListener("pointermove", onMove);
    document.removeEventListener("pointerleave", onLeave);
    cancelAnimationFrame(raf);
  };
}
