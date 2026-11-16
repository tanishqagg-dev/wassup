export type TickState = "none" | "sent" | "delivered" | "read";

/** WhatsApp-style receipt that draws itself: one grey tick, two grey ticks, two blue ticks. */
export function Ticks({ state, size = 16, label }: { state: TickState; size?: number; label?: string }) {
  return (
    <svg
      className="ticks"
      data-state={state}
      width={size * 1.15}
      height={size * 0.75}
      viewBox="0 0 23 15"
      role={label ? "img" : undefined}
      aria-label={label}
      aria-hidden={label ? undefined : true}
    >
      <path className="t1" d="M1.5 8.2 5.3 12 12.6 3" pathLength={1} />
      <path className="t2" d="M9.4 11.1 10.3 12 17.6 3" pathLength={1} />
    </svg>
  );
}
