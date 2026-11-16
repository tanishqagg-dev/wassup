# wassup.dev site

The download page for wassup. Vite + React, GSAP (ScrollTrigger, SplitText) with Lenis, and a live Three.js double tick. No images: every visual is real HTML UI, SVG or 3D rendered in the browser.

```bash
npm install
npm run dev       # http://localhost:5173
npm run build     # static output in dist/
```

Deploy `dist/` anywhere static (Vercel, Netlify, GitHub Pages). The install commands assume the Homebrew tap and GitHub repo from `../docs/releasing.md` exist.

The hero demo is scripted: tapping "yes 4821" on the phone plays the approval flow that wassup really runs (`drafts.notify`, a reply from your phone, then `wait_for_reply`).
