import { useId } from "react";

export const BUBBLE = "M34 15h32a20 20 0 0 1 20 20v17a20 20 0 0 1-20 20H41l-15.5 12.4c-1.6 1.3-3.9-.3-3.2-2.3L26 70.5A20 20 0 0 1 14 52V35a20 20 0 0 1 20-20z";
export const W = "M29 37 37.5 53 50 37 62.5 53 71 37";

/**
 * The wassup mark: a speech bubble with two equal ticks cut out of it, which read as a w.
 * draw: the w cuts itself out on mount. read: blue light fills the cut, like a read receipt.
 */
export function Mark({ size = 28, draw = false, read = false, mono = false, className = "" }: { size?: number; draw?: boolean; read?: boolean; mono?: boolean; className?: string }) {
  const id = useId().replace(/[^a-zA-Z0-9]/g, "");
  return (
    <svg className={`mark ${className}`} data-draw={draw} data-read={read} width={size} height={size} viewBox="0 0 100 100" aria-hidden>
      <defs>
        <mask id={`m${id}`} maskUnits="userSpaceOnUse" x="0" y="0" width="100" height="100">
          <rect width="100" height="100" fill="#fff" />
          <path className="mark-cut" d={W} pathLength={1} fill="none" stroke="#000" strokeWidth="8" strokeLinecap="round" strokeLinejoin="round" />
        </mask>
      </defs>
      <g mask={`url(#m${id})`}>
        <path d={BUBBLE} fill={mono ? "currentColor" : "var(--green)"} />
      </g>
      <path className="mark-ink" d={W} pathLength={1} fill="none" strokeWidth="8" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  );
}
