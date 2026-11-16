import { Magnetic } from "./Magnetic";

export const REPO = "https://github.com/tanishqagg-dev/wassup";
export const DMG = `${REPO}/releases/latest/download/wassup.dmg`;
/** Flip to true once a release has a notarized wassup.dmg attached (see docs/releasing.md). */
export const MAC_APP_READY = false;
/** Homebrew asks you to trust a third-party tap once before it loads it. */
export const BREW = "brew tap tanishqagg-dev/tap && brew trust tanishqagg-dev/tap && brew install wassup";

/** The one green thing on the page. */
export function DownloadButton({ small = false }: { small?: boolean }) {
  return (
    <Magnetic strength={small ? 0.18 : 0.28}>
      <a className="btn-dl" data-small={small} href={MAC_APP_READY ? DMG : "#download"}>
        <span className="btn-dl-inner">
          <svg className="dl-icon" width="18" height="18" viewBox="0 0 18 18" aria-hidden>
            <path className="dl-arrow" d="M9 2.5v8.5M5 7.5l4 4 4-4" />
            <path d="M3 13.5v.5A1.5 1.5 0 0 0 4.5 15.5h9A1.5 1.5 0 0 0 15 14v-.5" />
          </svg>
          {MAC_APP_READY ? (small ? "Download" : "Download for Mac") : "Get wassup"}
        </span>
      </a>
    </Magnetic>
  );
}
