import { useEffect, useRef, useState } from "react";
import { gsap, ScrollTrigger, useGSAP, prefersReducedMotion, scrollToTarget } from "../lib/motion";
import { QR_PATH, QR_SIZE } from "../lib/qr";
import { Mark } from "./Logo";
import { Command } from "./Command";

const STEPS = [
  { t: "Link your phone", d: "Scan a code with WhatsApp, the same way WhatsApp Web links. Or type a code on your phone instead." },
  { t: "Keep it running", d: "One switch. wassup starts quietly with your Mac, so your AI can reach WhatsApp any time." },
  { t: "Connect your AI", d: "It finds Claude, Codex and Cursor on your Mac. One click each." },
  { t: "Choose how careful", d: "Ask before every message, only for new chats, or never. Change it whenever you like." },
];

const APPS = [
  { name: "Claude", note: "Quit and reopen Claude to finish", hue: "#d97757" },
  { name: "Claude Code", note: "Also taught it the command line", hue: "#c96442" },
  { name: "Codex", note: "Ready in new sessions", hue: "#e8e8e8" },
  { name: "Cursor", note: "Not installed", hue: "#8a8a8a", missing: true },
];

const MODES = [
  { t: "Ask me first", d: "Your AI asks before every message it sends.", tag: "Recommended" },
  { t: "Ask for new chats", d: "It asks before messaging someone new or a group." },
  { t: "Don't ask", d: "It sends on its own. Rate limits still apply." },
];

function Qr() {
  return (
    <svg className="qr" viewBox={`-2 -2 ${QR_SIZE + 4} ${QR_SIZE + 4}`} shapeRendering="crispEdges" role="img" aria-label="QR code (this one opens the wassup GitHub page)">
      <rect x="-2" y="-2" width={QR_SIZE + 4} height={QR_SIZE + 4} fill="#fff" />
      <path d={QR_PATH} fill="#07100c" />
    </svg>
  );
}

/** Rows connect one after another while step 3 is on screen. */
function useSequence(active: boolean, count: number, gap: number) {
  const [n, setN] = useState(0);
  useEffect(() => {
    if (!active) {
      setN(0);
      return;
    }
    if (prefersReducedMotion()) {
      setN(count);
      return;
    }
    let i = 0;
    const id = window.setInterval(() => {
      i += 1;
      setN(i);
      if (i >= count) window.clearInterval(id);
    }, gap);
    return () => window.clearInterval(id);
  }, [active, count, gap]);
  return n;
}

function Window({ step }: { step: number }) {
  const connected = useSequence(step === 2, 3 * 2, 520);
  return (
    <div className="mac lit" aria-label={`The wassup Mac app, step ${step + 1}: ${STEPS[step].t}`} role="img">
      <div className="mac-bar" aria-hidden>
        <i />
        <i />
        <i />
        <div className="mac-pills">
          {STEPS.map((_, i) => (
            <span key={i} data-on={i === step} data-done={i < step} />
          ))}
        </div>
      </div>
      <div className="mac-body" aria-hidden>
        {/* 1: link */}
        <div className="pane" data-active={step === 0}>
          <h4>Link your phone</h4>
          <div className="link-grid">
            <div className="qr-card">
              <Qr />
              <svg className="ring" viewBox="0 0 36 36">
                <circle cx="18" cy="18" r="15" />
                <circle className="ring-fill" cx="18" cy="18" r="15" pathLength={1} />
              </svg>
              <span className="qr-note">New code in 42s</span>
            </div>
            <ol className="how">
              <li>
                <b>1</b>Open WhatsApp on your phone
              </li>
              <li>
                <b>2</b>Settings › Linked devices
              </li>
              <li>
                <b>3</b>Link a device, then point it here
              </li>
            </ol>
          </div>
          <span className="alt">Use phone number instead</span>
        </div>

        {/* 2: keep running */}
        <div className="pane" data-active={step === 1}>
          <h4>Keep it running</h4>
          <div className="toggle-card">
            <div>
              <b>Start wassup when I log in</b>
              <span>Runs quietly in the background. Uses almost nothing.</span>
            </div>
            <span className="switch" data-on={step === 1} />
          </div>
          <div className="toggle-card">
            <div>
              <b>Tell me when my AI wants to send</b>
              <span>A notification with Approve and Decline, right on your Mac.</span>
            </div>
            <span className="switch" data-on={step === 1} />
          </div>
          <div className="linked">
            <svg viewBox="0 0 24 24">
              <circle cx="12" cy="12" r="10" />
              <path d="M7.5 12.5 10.5 15.5 16.5 9" pathLength={1} />
            </svg>
            Linked as Tanishq
          </div>
        </div>

        {/* 3: connect */}
        <div className="pane" data-active={step === 2}>
          <h4>Connect your AI apps</h4>
          <ul className="apps">
            {APPS.map((a, i) => {
              const state = a.missing ? "missing" : connected >= i * 2 + 2 ? "done" : connected >= i * 2 + 1 ? "busy" : "idle";
              return (
                <li key={a.name} data-state={state}>
                  <span className="app-icon" style={{ ["--hue" as string]: a.hue }}>
                    {a.name[0]}
                  </span>
                  <span className="app-name">
                    <b>{a.name}</b>
                    <span>{state === "done" || a.missing ? a.note : "Found on this Mac"}</span>
                  </span>
                  <span className="app-btn">
                    <span className="lbl-connect">Connect</span>
                    <span className="spin" />
                    <span className="lbl-done">
                      <svg viewBox="0 0 16 16">
                        <path d="M3.5 8.5 6.5 11.5 12.5 4.5" pathLength={1} />
                      </svg>
                      Connected
                    </span>
                    <span className="lbl-missing">Get it</span>
                  </span>
                </li>
              );
            })}
          </ul>
        </div>

        {/* 4: safety */}
        <div className="pane" data-active={step === 3}>
          <h4>How careful should your AI be?</h4>
          <div className="modes">
            {MODES.map((m, i) => (
              <div className="mode" key={m.t} data-on={i === 0}>
                <span className="radio">
                  <svg viewBox="0 0 16 16">
                    <path d="M4 8.5 7 11.5 12.5 5" pathLength={1} />
                  </svg>
                </span>
                <b>{m.t}</b>
                <span>{m.d}</span>
                {m.tag && <em>{m.tag}</em>}
              </div>
            ))}
          </div>
          <p className="mode-note">Approvals show up in your menu bar, as a notification, and in your WhatsApp "Message yourself" chat.</p>
        </div>
      </div>
    </div>
  );
}

export function SetupShowcase() {
  const sec = useRef<HTMLElement>(null);
  const [step, setStep] = useState(0);
  const st = useRef<ScrollTrigger | null>(null);
  const pinned = useRef(false);

  useGSAP(
    () => {
      const mm = gsap.matchMedia();
      mm.add("(min-width: 961px) and (prefers-reduced-motion: no-preference)", () => {
        pinned.current = true;
        st.current = ScrollTrigger.create({
          trigger: sec.current,
          start: "top top",
          end: "+=260%",
          pin: true,
          scrub: true,
          onUpdate: (s) => {
            sec.current?.style.setProperty("--p", s.progress.toFixed(4));
            setStep(Math.min(STEPS.length - 1, Math.floor(s.progress * STEPS.length)));
          },
        });
        return () => {
          pinned.current = false;
          st.current = null;
        };
      });
      return () => mm.revert();
    },
    { scope: sec },
  );

  const go = (i: number) => {
    const t = st.current;
    if (pinned.current && t) {
      scrollToTarget(t.start + ((i + 0.5) / STEPS.length) * (t.end - t.start));
    } else {
      setStep(i);
    }
  };

  return (
    <section className="setup" ref={sec} id="setup" aria-label="Set up the Mac app">
      <div className="setup-inner">
        <div className="setup-copy">
          <Mark size={40} className="setup-mark" />
          <h2 className="display" data-reveal>
            Set up in a minute. No Terminal.
          </h2>
          <p className="lede">The Mac app walks you through it in plain words. Then it lives in your menu bar, holding anything your AI wants to send until you say yes.</p>
          <ol className="setup-steps">
            {STEPS.map((s, i) => (
              <li key={s.t} data-on={i === step} data-done={i < step}>
                <button type="button" onClick={() => go(i)} aria-current={i === step ? "step" : undefined}>
                  <span className="n">{i + 1}</span>
                  <span className="txt">
                    <b>{s.t}</b>
                    <span>{s.d}</span>
                  </span>
                </button>
              </li>
            ))}
          </ol>
          <div className="setup-alt">
            <span>Rather use Terminal? Same four questions:</span>
            <Command cmd="wassup setup" size="sm" />
          </div>
        </div>
        <div className="setup-window">
          <Window step={step} />
        </div>
      </div>
    </section>
  );
}
