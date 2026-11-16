import { useEffect, useRef, useState } from "react";
import { gsap, ScrollTrigger, SplitText, useGSAP, prefersReducedMotion, RISE, startSmoothScroll, startPointerLight } from "./lib/motion";
import { HeroDemo } from "./components/HeroDemo";
import { Statement, Track, ToolIndex, Safety, Terminal } from "./components/Sections";
import { SetupShowcase } from "./components/SetupShowcase";
import { Command } from "./components/Command";
import { Mark } from "./components/Logo";
import { DownloadButton, MAC_APP_READY, REPO } from "./components/Download";
import { Ticks, type TickState } from "./components/Ticks";

function Nav() {
  const [scrolled, setScrolled] = useState(false);
  const [hidden, setHidden] = useState(false);
  useEffect(() => {
    let lastY = window.scrollY;
    const onScroll = () => {
      const y = window.scrollY;
      setScrolled(y > 12);
      // Hide while reading down, come back the moment you scroll up.
      if (Math.abs(y - lastY) > 6) {
        setHidden(y > lastY && y > 480);
        lastY = y;
      }
    };
    onScroll();
    window.addEventListener("scroll", onScroll, { passive: true });
    return () => window.removeEventListener("scroll", onScroll);
  }, []);
  return (
    <header className="nav" data-scrolled={scrolled} data-hidden={hidden}>
      <a className="wordmark" href="#top" aria-label="wassup, back to top">
        <Mark size={24} />
        wassup
      </a>
      <nav className="nav-links" aria-label="Main">
        <a className="hide-sm" href="#tools">
          What it does
        </a>
        <a className="hide-sm" href="#safety">
          Safety
        </a>
        <a className="hide-sm" href="#setup">
          Setup
        </a>
        <a className="hide-sm" href={REPO}>
          GitHub
        </a>
        <DownloadButton small />
      </nav>
    </header>
  );
}

function Hero() {
  const ref = useRef<HTMLElement>(null);
  useGSAP(
    () => {
      const mm = gsap.matchMedia();
      mm.add("(prefers-reduced-motion: no-preference)", () => {
        let split: SplitText | null = null;
        const intro = gsap.timeline({ paused: true, delay: 0.05 });
        document.fonts.ready.then(() => {
          split = SplitText.create(".hero h1 .ln", { type: "lines", mask: "lines", linesClass: "line" });
          intro
            .from(".hero-mark", { scale: 0.6, opacity: 0, duration: 1.1, ease: "back.out(1.7)" })
            .from(split.lines, { yPercent: 110, duration: 1.1, ease: RISE, stagger: 0.08 }, "-=0.8")
            .from(".hero-sub", { y: 22, opacity: 0, duration: 1, ease: RISE }, "-=0.8")
            .from(".hero-actions > *", { y: 16, opacity: 0, duration: 0.9, ease: RISE, stagger: 0.08 }, "-=0.8")
            .from(".hero .fine", { opacity: 0, duration: 0.8 }, "-=0.6")
            .from(".stage", { y: 140, opacity: 0, duration: 1.5, ease: RISE }, "-=1.1")
            .play();
        });

        return () => split?.revert();
      });
      // The stage rises out of the dark and turns toward you as you scroll (wide screens only).
      mm.add("(prefers-reduced-motion: no-preference) and (min-width: 861px)", () => {
        const scrub = { trigger: ".stage-3d", start: "top 100%", end: "top 12%", scrub: 0.8 };
        gsap.fromTo(".stage-3d", { rotateX: 24, scale: 0.9, y: 30 }, { rotateX: 0, scale: 1, y: 0, ease: "none", scrollTrigger: scrub });
        gsap.fromTo(".stage-3d .phone", { rotateY: -26, rotateX: 6, z: -60 }, { rotateY: -7, rotateX: 0, z: 0, ease: "none", scrollTrigger: scrub });
        gsap.fromTo(".stage-3d .claude", { rotateY: 14, z: -120 }, { rotateY: 0, z: 0, ease: "none", scrollTrigger: scrub });
        gsap.to(".hero-head", {
          y: -60,
          opacity: 0.15,
          ease: "none",
          scrollTrigger: { trigger: ref.current, start: "top top", end: "45% top", scrub: true },
        });
      });
      return () => mm.revert();
    },
    { scope: ref },
  );
  return (
    <section className="hero" ref={ref} id="top">
      <div className="hero-head">
        <Mark size={76} draw className="hero-mark" />
        <h1 className="display">
          <span className="ln">Your AI can text now.</span> <span className="ln soft">You keep the last word.</span>
        </h1>
        <p className="hero-sub">wassup gives Claude and other AI apps your WhatsApp. They read, reply and wait for answers. Nothing goes out until you tap yes.</p>
        <div className="hero-actions">
          <DownloadButton />
          <Command cmd="brew install tanishqagg-dev/tap/wassup" />
        </div>
        <p className="fine">
          Free and open source · macOS 14 or later · also on <a href="#download">Linux and Windows</a>
        </p>
      </div>
      <div className="stage-wrap">
        <div className="stage-3d">
          <HeroDemo />
        </div>
      </div>
    </section>
  );
}

const PLATFORMS = [
  {
    t: "Mac app",
    d: MAC_APP_READY
      ? "A setup window for linking and connecting, then approvals in your menu bar."
      : "A setup window for linking and connecting, then approvals in your menu bar. The notarized download is on its way; until then, use Homebrew and run wassup setup.",
    cmd: null,
  },
  {
    t: "Homebrew",
    d: "The command line and MCP server on its own. Then run wassup setup.",
    cmd: "brew install tanishqagg-dev/tap/wassup",
  },
  {
    t: "Linux",
    d: "Checks the download against its checksum before installing. Windows builds are on Releases.",
    cmd: "curl -fsSL https://raw.githubusercontent.com/tanishqagg-dev/wassup/main/install.sh | sh",
  },
];

function Payoff() {
  const ref = useRef<HTMLElement>(null);
  const [read, setRead] = useState(prefersReducedMotion());
  useGSAP(
    () => {
      ScrollTrigger.create({ trigger: ".payoff-mark", start: "top 70%", onEnter: () => setRead(true), onLeaveBack: () => setRead(false) });
    },
    { scope: ref },
  );
  return (
    <section className="payoff" ref={ref} id="download" aria-label="Download wassup">
      <Mark size={156} read={read} className="payoff-mark" />
      <h2 className="display" data-reveal>
        Two blue ticks.
      </h2>
      <p className="lede">You read the whole thing. Now let your AI handle the small messages, and say yes to the ones that go out.</p>
      <div className="hero-actions">{MAC_APP_READY ? <DownloadButton /> : <Command cmd="brew install tanishqagg-dev/tap/wassup" />}</div>
      <div className="platforms" data-fade>
        {PLATFORMS.map((p) => (
          <div className="plat lit" key={p.t}>
            <h3>{p.t}</h3>
            <p>{p.d}</p>
            {p.cmd ? (
              <Command cmd={p.cmd} size="sm" />
            ) : (
              <a className="plat-link" href={`${REPO}/releases/latest`}>
                {MAC_APP_READY ? "All downloads and checksums" : "Releases on GitHub"}
              </a>
            )}
          </div>
        ))}
      </div>
    </section>
  );
}

function ReadState() {
  const [state, setState] = useState<TickState>("sent");
  useEffect(() => {
    const st = ScrollTrigger.create({
      start: 0,
      end: "max",
      onUpdate: (s) => setState(s.progress > 0.96 ? "read" : s.progress > 0.1 ? "delivered" : "sent"),
    });
    return () => st.kill();
  }, []);
  return (
    <div className="readstate" aria-hidden>
      <Ticks state={state} size={14} />
      {state}
    </div>
  );
}

function Footer() {
  return (
    <footer className="colophon">
      <a className="wordmark" href="#top" aria-label="wassup, back to top">
        <Mark size={22} />
        wassup
      </a>
      <p className="fine">
        Open source (MIT) and unofficial. Not affiliated with WhatsApp or Meta. Built on whatsmeow. Your messages stay on your machine.
      </p>
      <nav aria-label="Footer">
        <a href={REPO}>GitHub</a>
        <a href={`${REPO}#readme`}>Docs</a>
        <a href={`${REPO}/blob/main/docs/configuration.md`}>Configuration</a>
        <a href={`${REPO}/blob/main/docs/releasing.md`}>Verify a release</a>
      </nav>
    </footer>
  );
}

/** Section headlines rise line by line; [data-fade] groups rise child by child. */
function useReveals() {
  useGSAP(() => {
    const mm = gsap.matchMedia();
    mm.add("(prefers-reduced-motion: no-preference)", () => {
      const splits: SplitText[] = [];
      document.fonts.ready.then(() => {
        gsap.utils.toArray<HTMLElement>("[data-reveal]").forEach((el) => {
          const split = SplitText.create(el, { type: "lines", mask: "lines", linesClass: "line" });
          splits.push(split);
          gsap.from(split.lines, {
            yPercent: 110,
            duration: 1.05,
            ease: RISE,
            stagger: 0.07,
            scrollTrigger: { trigger: el, start: "top 82%", toggleActions: "play none none none" },
          });
        });
        gsap.utils.toArray<HTMLElement>("[data-fade]").forEach((el) => {
          gsap.from(el.children, {
            y: 50,
            opacity: 0,
            duration: 1,
            ease: RISE,
            stagger: 0.08,
            scrollTrigger: { trigger: el, start: "top 85%", toggleActions: "play none none none" },
          });
        });
        ScrollTrigger.refresh();
      });
      return () => splits.forEach((s) => s.revert());
    });
    return () => mm.revert();
  });
}

export default function App() {
  useEffect(() => {
    const stopScroll = startSmoothScroll();
    const stopLight = startPointerLight();
    document.fonts.ready.then(() => ScrollTrigger.refresh());
    return () => {
      stopScroll();
      stopLight();
    };
  }, []);
  useReveals();
  return (
    <>
      <div className="grain" aria-hidden />
      <Nav />
      <main>
        <Hero />
        <Statement />
        <Track />
        <ToolIndex />
        <Safety />
        <SetupShowcase />
        <Terminal />
        <Payoff />
      </main>
      <Footer />
      <ReadState />
    </>
  );
}
