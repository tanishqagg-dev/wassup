import { useCallback, useEffect, useRef, useState } from "react";
import { Ticks, type TickState } from "./Ticks";
import { prefersReducedMotion } from "../lib/motion";

type Phase = "idle" | "asked" | "drafting" | "pending" | "approved" | "declined";

const DRAFT = "Running 10 min late! Need anything from the store?";

function wait(ms: number) {
  return new Promise<void>((r) => window.setTimeout(r, ms));
}

function StatusBar() {
  return (
    <div className="statusbar" aria-hidden>
      <span>18:42</span>
      <svg viewBox="0 0 46 11">
        <rect x="0" y="7" width="3" height="4" rx="1" fill="currentColor" />
        <rect x="5" y="5" width="3" height="6" rx="1" fill="currentColor" />
        <rect x="10" y="2.5" width="3" height="8.5" rx="1" fill="currentColor" />
        <rect x="15" y="0" width="3" height="11" rx="1" fill="currentColor" />
        <rect x="24" y="1" width="19" height="9" rx="2.5" fill="none" stroke="currentColor" strokeOpacity=".5" />
        <rect x="26" y="3" width="13" height="5" rx="1.2" fill="currentColor" />
        <rect x="44" y="4" width="1.6" height="3" rx=".8" fill="currentColor" fillOpacity=".5" />
      </svg>
    </div>
  );
}

export function HeroDemo() {
  const [phase, setPhase] = useState<Phase>("idle");
  const [aiLine, setAiLine] = useState(false);
  const [tool, setTool] = useState(false);
  const [receipt, setReceipt] = useState<TickState>("none");
  const [waiting, setWaiting] = useState(false);
  const [reply, setReply] = useState(false);
  const [phoneSys, setPhoneSys] = useState(false);
  const run = useRef(0);
  const stageRef = useRef<HTMLDivElement>(null);
  const fast = prefersReducedMotion();
  const t = (ms: number) => (fast ? Math.min(ms, 250) : ms);

  const start = useCallback(async () => {
    const id = ++run.current;
    const alive = () => run.current === id;
    setPhase("asked");
    setAiLine(false);
    setTool(false);
    setReceipt("none");
    setWaiting(false);
    setReply(false);
    setPhoneSys(false);
    await wait(t(900));
    if (!alive()) return;
    setAiLine(true);
    setPhase("drafting");
    await wait(t(900));
    if (!alive()) return;
    setTool(true);
    await wait(t(700));
    if (!alive()) return;
    setPhase("pending");
  }, [fast]);

  // Start once the stage is on screen.
  useEffect(() => {
    const el = stageRef.current;
    if (!el) return;
    const io = new IntersectionObserver(
      (entries) => {
        if (entries.some((e) => e.isIntersecting)) {
          io.disconnect();
          start();
        }
      },
      { threshold: 0.25 },
    );
    io.observe(el);
    return () => io.disconnect();
  }, [start]);

  const decide = async (yes: boolean) => {
    if (phase !== "pending") return;
    const id = run.current;
    const alive = () => run.current === id;
    setPhase(yes ? "approved" : "declined");
    await wait(t(650));
    if (!alive()) return;
    setPhoneSys(true);
    if (!yes) return;
    setReceipt("sent");
    await wait(t(800));
    if (!alive()) return;
    setReceipt("delivered");
    await wait(t(1300));
    if (!alive()) return;
    setReceipt("read");
    setWaiting(true);
    await wait(t(2200));
    if (!alive()) return;
    setWaiting(false);
    setReply(true);
  };

  const pending = phase === "pending";
  const decided = phase === "approved" || phase === "declined";

  return (
    <div className="stage" ref={stageRef}>
      <svg className="stage-path" viewBox="0 0 1000 600" preserveAspectRatio="none" aria-hidden>
        <path
          d="M 560 390 C 610 360, 600 300, 660 290"
          style={{ opacity: tool ? 0.55 : 0, transition: "opacity 500ms" }}
          vectorEffect="non-scaling-stroke"
        />
      </svg>

      {/* Claude */}
      <section className="claude" aria-label="Claude, using wassup">
        <div className="claude-bar">
          <strong>
            <span className="claude-mark" aria-hidden>
              <svg width="14" height="14" viewBox="0 0 14 14">
                <path d="M7 1.5v11M1.5 7h11M3 3l8 8M11 3l-8 8" />
              </svg>
            </span>
            Claude
          </strong>
          <span className="connected">
            <i aria-hidden />
            whatsapp (wassup)
          </span>
        </div>
        <div className="claude-log" aria-live="polite">
          {phase !== "idle" && <div className="c-user pop">Tell Mom I'm running 10 minutes late, and ask if she needs anything from the store.</div>}
          {aiLine && (
            <div className="c-ai pop">
              <p>I'll send that to Mom on WhatsApp.</p>
            </div>
          )}
          {tool && (
            <div className="toolcall pop">
              <div className="toolcall-head">
                <span>
                  <b>send_message</b> · whatsapp
                </span>
                <Ticks state={receipt} size={14} label={receipt === "none" ? undefined : `message ${receipt}`} />
              </div>
              <pre>
                <span className="k">to</span>: <span className="s">"Mom"</span>
                {"\n"}
                <span className="k">text</span>: <span className="s">"{DRAFT}"</span>
              </pre>
              {pending && (
                <div className="toolstate">
                  <span className="dots" aria-hidden>
                    <i />
                    <i />
                    <i />
                  </span>
                  Waiting for your approval on your phone
                </div>
              )}
              {phase === "approved" && (
                <div className="toolstate" data-tone="ok">
                  Approved on your phone. Sent to Mom at 18:42
                  {receipt === "read" ? ", read." : "."}
                </div>
              )}
              {phase === "declined" && (
                <div className="toolstate" data-tone="no">
                  You declined on your phone. Nothing was sent.
                </div>
              )}
            </div>
          )}
          {phase === "declined" && phoneSys && (
            <div className="c-ai pop">
              <p>Okay, I won't send it.</p>
            </div>
          )}
          {(waiting || reply) && (
            <div className="toolcall pop">
              <div className="toolcall-head">
                <span>
                  <b>wait_for_reply</b> · whatsapp
                </span>
              </div>
              {waiting ? (
                <div className="toolstate">
                  <span className="dots" aria-hidden>
                    <i />
                    <i />
                    <i />
                  </span>
                  Waiting for Mom
                </div>
              ) : (
                <pre>
                  <span className="k">Mom</span>: <span className="s">"Bread and eggs please. Drive safe!"</span>
                </pre>
              )}
            </div>
          )}
          {reply && (
            <div className="c-ai pop">
              <p>Mom says bread and eggs, and drive safe.</p>
            </div>
          )}
        </div>
        <div className="claude-input">
          <span>Reply to Claude</span>
          {decided && (
            <button type="button" className="replay" onClick={start}>
              <svg width="14" height="14" viewBox="0 0 14 14" aria-hidden>
                <path d="M2.5 7a4.5 4.5 0 1 0 1.3-3.2M2.5 2v2.5H5" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" />
              </svg>
              Play again
            </button>
          )}
        </div>
      </section>

      {/* The phone: the signature object */}
      <div className="phone-wrap">
        <div className="phone" role="group" aria-label="Your phone, Message yourself chat">
          <div className="screen">
            <span className="island" aria-hidden />
            <StatusBar />
            <div className="wa-head">
              <svg width="10" height="16" viewBox="0 0 10 16" aria-hidden>
                <path d="M8 2 2 8l6 6" fill="none" stroke="var(--ink)" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" />
              </svg>
              <span className="avatar" aria-hidden>
                ME
              </span>
              <span className="who">
                <b>You</b>
                <span>Message yourself</span>
              </span>
            </div>
            <div className="wa-chat">
              <span className="day">Today</span>
              <div className="bubble out">
                Pick up the dry cleaning
                <span className="meta">
                  09:14 <Ticks state="read" size={13} />
                </span>
              </div>
              {(pending || decided) && (
                <div className="bubble in pop">
                  <span className="from">wassup · approval #12</span>
                  Claude wants to message <b>Mom</b>:
                  <blockquote>{DRAFT}</blockquote>
                  Reply <b>yes 4821</b> to send it, or <b>no 4821</b> to cancel.
                  <span className="meta">18:42</span>
                </div>
              )}
              {pending && (
                <div className="quick pop">
                  <button type="button" className="yes" onClick={() => decide(true)}>
                    yes 4821
                  </button>
                  <button type="button" className="no" onClick={() => decide(false)}>
                    no 4821
                  </button>
                </div>
              )}
              {decided && (
                <div className="bubble out pop">
                  {phase === "approved" ? "yes 4821" : "no 4821"}
                  <span className="meta">
                    18:42 <Ticks state="read" size={13} />
                  </span>
                </div>
              )}
              {decided && phoneSys && (
                <div className="bubble in pop">
                  <span className="from">wassup</span>
                  {phase === "approved" ? "Done: #12 went through." : "Cancelled #12. Nothing was sent."}
                  <span className="meta">18:42</span>
                </div>
              )}
            </div>
            <div className="composer" aria-hidden>
              <span>Message</span>
              <i>
                <svg width="14" height="14" viewBox="0 0 14 14">
                  <path d="M7 2v6M4.5 5.5a2.5 2.5 0 0 0 5 0M7 10.5V12" />
                </svg>
              </i>
            </div>
          </div>
        </div>
        <div className="hint" data-off={!pending}>
          <svg width="70" height="44" viewBox="0 0 70 44" aria-hidden>
            <path d="M66 6c-14 2-34 6-48 22" />
            <path d="M13 20l4 9 9-3" />
          </svg>
          Go on, tap yes. This is how you approve what your AI sends.
        </div>
      </div>
    </div>
  );
}
