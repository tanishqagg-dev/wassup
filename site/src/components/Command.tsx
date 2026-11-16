import { useRef, useState } from "react";

/** A copyable shell command. The copy icon morphs into a check; no toast needed. */
export function Command({ cmd, label, size = "md" }: { cmd: string; label?: string; size?: "md" | "sm" }) {
  const [copied, setCopied] = useState(false);
  const timer = useRef(0);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(cmd);
    } catch {
      const t = document.createElement("textarea");
      t.value = cmd;
      document.body.appendChild(t);
      t.select();
      document.execCommand("copy");
      t.remove();
    }
    setCopied(true);
    window.clearTimeout(timer.current);
    timer.current = window.setTimeout(() => setCopied(false), 1800);
  };
  return (
    <div className="command lit" data-size={size}>
      <code>
        <span className="prompt" aria-hidden>
          $
        </span>
        <span className="cmd-text">{cmd}</span>
      </code>
      <button type="button" className="copy" data-copied={copied} onClick={copy} aria-label={label ?? `Copy: ${cmd}`}>
        <svg width="16" height="16" viewBox="0 0 16 16" aria-hidden>
          <g className="copy-icon">
            <rect x="5.5" y="5.5" width="8" height="8" rx="2" />
            <path d="M10.5 3.5v-.5a1.5 1.5 0 0 0-1.5-1.5H4A1.5 1.5 0 0 0 2.5 3v5A1.5 1.5 0 0 0 4 9.5h.5" />
          </g>
          <path className="copy-check" d="M3.5 8.5 6.5 11.5 12.5 4.5" pathLength={1} />
        </svg>
        <span className="copy-label" aria-live="polite">
          <span>Copy</span>
          <span>Copied</span>
        </span>
      </button>
    </div>
  );
}
